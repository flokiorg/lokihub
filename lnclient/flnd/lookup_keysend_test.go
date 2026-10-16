package flnd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/routerrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lnclient/flnd/wrapper"
)

// fakeLightning answers LookupInvoice; every other method panics via the nil
// embedded interface, so a test fails loudly if the code calls anything else.
type fakeLightning struct {
	lnrpc.LightningClient
	invoice *lnrpc.Invoice
	err     error
	asked   *lnrpc.PaymentHash
}

func (f *fakeLightning) LookupInvoice(_ context.Context, req *lnrpc.PaymentHash, _ ...grpc.CallOption) (*lnrpc.Invoice, error) {
	f.asked = req
	return f.invoice, f.err
}

// paymentStream replays updates, then err once they run out.
type paymentStream struct {
	grpc.ClientStream
	updates []*lnrpc.Payment
	err     error
}

func (s *paymentStream) Recv() (*lnrpc.Payment, error) {
	if len(s.updates) == 0 {
		if s.err == nil {
			return nil, errors.New("stream exhausted")
		}
		return nil, s.err
	}
	p := s.updates[0]
	s.updates = s.updates[1:]
	return p, nil
}

type fakeRouter struct {
	routerrpc.RouterClient
	send     *paymentStream
	sendErr  error
	track    *paymentStream
	trackErr error
	sent     *routerrpc.SendPaymentRequest
}

func (f *fakeRouter) SendPaymentV2(_ context.Context, req *routerrpc.SendPaymentRequest, _ ...grpc.CallOption) (routerrpc.Router_SendPaymentV2Client, error) {
	f.sent = req
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	return f.send, nil
}

func (f *fakeRouter) TrackPaymentV2(_ context.Context, _ *routerrpc.TrackPaymentRequest, _ ...grpc.CallOption) (routerrpc.Router_TrackPaymentV2Client, error) {
	if f.trackErr != nil {
		return nil, f.trackErr
	}
	return f.track, nil
}

func newFakeFLND(ln *fakeLightning, router *fakeRouter) *FLNDService {
	return &FLNDService{client: wrapper.NewFLNDWrapperWithClients(ln, router), ctx: context.Background()}
}

func TestLookupInvoice_MapsTheNodesInvoice(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	hashBytes, _ := hex.DecodeString(hash)
	ln := &fakeLightning{invoice: &lnrpc.Invoice{
		RHash: hashBytes, RPreimage: []byte{1, 2}, ValueMsat: 21_000, Memo: "coffee",
		State: lnrpc.Invoice_SETTLED, SettleDate: 1700, CreationDate: 1600, Expiry: 60,
	}}
	tx, err := newFakeFLND(ln, &fakeRouter{}).LookupInvoice(context.Background(), hash)
	require.NoError(t, err)
	assert.Equal(t, hashBytes, ln.asked.RHash, "asked the node for the hash given")
	assert.Equal(t, hash, tx.PaymentHash)
	assert.Equal(t, int64(21_000), tx.Amount)
	assert.Equal(t, "0102", tx.Preimage)
	require.NotNil(t, tx.SettledAt)
	assert.Equal(t, int64(1700), *tx.SettledAt)
	require.NotNil(t, tx.ExpiresAt)
	assert.Equal(t, int64(1660), *tx.ExpiresAt)
}

func TestLookupInvoice_RejectsABadHashWithoutAskingTheNode(t *testing.T) {
	for _, hash := range []string{"zz", strings.Repeat("ab", 31)} {
		ln := &fakeLightning{}
		_, err := newFakeFLND(ln, &fakeRouter{}).LookupInvoice(context.Background(), hash)
		assert.Error(t, err, "hash %q", hash)
		assert.Nil(t, ln.asked, "hash %q reached the node", hash)
	}
}

func TestLookupInvoice_PassesTheNodesErrorThrough(t *testing.T) {
	want := status.Error(codes.NotFound, "unable to locate invoice")
	_, err := newFakeFLND(&fakeLightning{err: want}, &fakeRouter{}).LookupInvoice(context.Background(), strings.Repeat("ab", 32))
	assert.ErrorIs(t, err, want)
}

const keysendPreimage = "0102030405060708091011121314151617181920212223242526272829303132"

func TestSendKeysend_BuildsTheRequestAndReturnsTheFee(t *testing.T) {
	router := &fakeRouter{send: &paymentStream{updates: []*lnrpc.Payment{
		{Status: lnrpc.Payment_IN_FLIGHT},
		{Status: lnrpc.Payment_SUCCEEDED, PaymentPreimage: keysendPreimage, FeeMsat: 7},
	}}}
	dest := strings.Repeat("02", 33)
	resp, err := newFakeFLND(&fakeLightning{}, router).SendKeysend(5_000, dest,
		[]lnclient.TLVRecord{{Type: 696969, Value: "beef"}}, keysendPreimage)
	require.NoError(t, err)
	assert.Equal(t, uint64(7), resp.Fee)

	pre, _ := hex.DecodeString(keysendPreimage)
	hash := sha256.Sum256(pre)
	destBytes, _ := hex.DecodeString(dest)
	req := router.sent
	assert.Equal(t, destBytes, req.Dest)
	assert.Equal(t, int64(5_000), req.AmtMsat)
	assert.Equal(t, hash[:], req.PaymentHash, "the hash is the preimage's, so the payee can settle")
	assert.Equal(t, pre, req.DestCustomRecords[5482373484], "keysend carries its preimage in record 5482373484")
	assert.Equal(t, []byte{0xbe, 0xef}, req.DestCustomRecords[696969])
}

func TestSendKeysend_AFailedPaymentIsAFailure(t *testing.T) {
	router := &fakeRouter{send: &paymentStream{updates: []*lnrpc.Payment{
		{Status: lnrpc.Payment_FAILED, FailureReason: lnrpc.PaymentFailureReason_FAILURE_REASON_NO_ROUTE},
	}}}
	_, err := newFakeFLND(&fakeLightning{}, router).SendKeysend(5_000, strings.Repeat("02", 33), nil, keysendPreimage)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NO_ROUTE")
}

func TestSendKeysend_AMismatchedPreimageIsAnError(t *testing.T) {
	router := &fakeRouter{send: &paymentStream{updates: []*lnrpc.Payment{
		{Status: lnrpc.Payment_SUCCEEDED, PaymentPreimage: strings.Repeat("00", 32)},
	}}}
	_, err := newFakeFLND(&fakeLightning{}, router).SendKeysend(5_000, strings.Repeat("02", 33), nil, keysendPreimage)
	require.Error(t, err)
}

// A broken stream is not a failed payment: the node is asked, and what it says
// decides — paid, definitely failed, or still pending.
func TestSendKeysend_AfterABrokenStreamTheNodeDecides(t *testing.T) {
	broken := errors.New("stream broke")
	cases := []struct {
		name      string
		track     *paymentStream
		trackErr  error
		wantFee   uint64
		wantErrIs error
	}{
		{name: "paid", track: &paymentStream{updates: []*lnrpc.Payment{
			{Status: lnrpc.Payment_SUCCEEDED, PaymentPreimage: keysendPreimage, FeeMsat: 3},
		}}, wantFee: 3},
		{name: "never started", trackErr: status.Error(codes.NotFound, "no payment"), wantErrIs: broken},
		{name: "unknown", trackErr: errors.New("node unreachable"), wantErrIs: lnclient.ErrPaymentPending},
		{name: "in flight", track: &paymentStream{updates: []*lnrpc.Payment{{Status: lnrpc.Payment_IN_FLIGHT}}},
			wantErrIs: lnclient.ErrPaymentPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			router := &fakeRouter{send: &paymentStream{err: broken}, track: c.track, trackErr: c.trackErr}
			resp, err := newFakeFLND(&fakeLightning{}, router).SendKeysend(5_000, strings.Repeat("02", 33), nil, keysendPreimage)
			if c.wantErrIs != nil {
				require.ErrorIs(t, err, c.wantErrIs)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.wantFee, resp.Fee)
		})
	}
}
