package interceptor

import (
	"testing"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// T1-4: characterization tests for Interceptor.Intercept() itself, as
// opposed to resolvePayment/openChannel (already well covered elsewhere).
// These are deliberately a tripwire pinning CURRENT observable behavior at
// Intercept()'s own entry point — not exhaustive path coverage of every
// nested branch (in particular, the JIT-channel-open + confirmation-poll
// path, which needs real/mocked time delays, is out of scope here and
// covered separately by openChannel's own tests). If a later refactor
// changes what one of these asserts, that's a decision to make
// deliberately, not something that should fail silently.

func newCharacterizationInterceptor(store *MockStore, client *MockLightningClient, notifier *MockNotifier) *Interceptor {
	return &Interceptor{
		config:              config.NewNodeConfigRef(&config.NodeConfig{TimeLockDelta: 40}),
		client:              client,
		store:               store,
		notificationService: notifier,
	}
}

// fakePubkey builds a fixed-size (33-byte, compressed-pubkey-shaped) fake
// destination, filled with the given byte — Intercept() logs a warning for
// any non-33-byte "destination" on a registered payment (a real sanity
// check on real pubkeys), which a shorter placeholder would trip on every
// registered-payment test case here for no reason.
func fakePubkey(fill byte) []byte {
	b := make([]byte, 33)
	for i := range b {
		b[i] = fill
	}
	return b
}

func testInterceptRequest() core.InterceptRequest {
	return core.InterceptRequest{
		Identifier:          "htlc-1",
		PaymentHash:         []byte{0xaa, 0xbb},
		IncomingAmountMloki: 100_000,
		OutgoingAmountMloki: 99_000,
		IncomingExpiry:      1000,
		OutgoingExpiry:      900,
	}
}

// TestIntercept_UnregisteredForward_UnknownNextHop_Resumes: a plain forward
// (not registered for any channel-open flow) whose scid isn't a known
// channel and isn't a registered destination either — nothing for flspd to
// do, resume the HTLC.
func TestIntercept_UnregisteredForward_UnknownNextHop_Resumes(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	req := testInterceptRequest()

	// Non-empty paymentHash + no error skips the LSPS2/LSPS1 fallback in
	// resolvePayment entirely (see resolvePayment's `len(paymentHash) == 0
	// && token == ""` condition) — this is "found in payments table, but
	// with a nil paymentSecret", i.e. a plain forward, not registered for
	// any channel-open flow.
	store.On("PaymentInfo", mock.Anything).Return(
		"", "", nil, req.PaymentHash, nil, nil, int64(0), int64(0), nil, nil, nil,
	)
	client.On("GetPeerId", mock.Anything).Return(nil, nil)

	i := newCharacterizationInterceptor(store, client, nil)
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_RESUME, result.Action)
}

// TestIntercept_ResolvePaymentHardError_FailsTemporaryNodeFailure: if
// resolvePayment can't determine anything about this payment at all (a
// hard store error, with no fallback able to identify it either), the HTLC
// must fail rather than resume — resuming a payment flspd couldn't
// classify at all would be the more dangerous default.
func TestIntercept_ResolvePaymentHardError_FailsTemporaryNodeFailure(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	req := testInterceptRequest()

	store.On("PaymentInfo", mock.Anything).Return(
		"", "", nil, nil, nil, nil, int64(0), int64(0), nil, nil, assert.AnError,
	)
	store.On("GetLsps2Order", mock.Anything).Return(nil, assert.AnError)
	client.On("GetPeerId", mock.Anything).Return(nil, assert.AnError)

	i := newCharacterizationInterceptor(store, client, nil)
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_FAIL_HTLC_WITH_CODE, result.Action)
	assert.Equal(t, core.FAILURE_TEMPORARY_NODE_FAILURE, result.FailureCode)
}

// TestIntercept_GetPeerIdError_Resumes: if flspd can't identify the next
// hop for a plain (unregistered) forward, current behavior is to resume
// rather than fail the HTLC — flspd only intervenes in flows it recognizes.
func TestIntercept_GetPeerIdError_Resumes(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	req := testInterceptRequest()

	store.On("PaymentInfo", mock.Anything).Return(
		"", "", nil, req.PaymentHash, nil, nil, int64(0), int64(0), nil, nil, nil,
	)
	client.On("GetPeerId", mock.Anything).Return(nil, assert.AnError)

	i := newCharacterizationInterceptor(store, client, nil)
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_RESUME, result.Action)
}

// TestIntercept_RegisteredPayment_RoutedToDifferentNextHop_Resumes: a
// registered (channel-open) payment whose scid resolves to a DIFFERENT
// peer than the registered destination means flspd is an intermediate
// hop for this HTLC, not the last hop — just forward it.
func TestIntercept_RegisteredPayment_RoutedToDifferentNextHop_Resumes(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	req := testInterceptRequest()

	destination := fakePubkey(0x01)
	otherPeer := fakePubkey(0x02)
	store.On("PaymentInfo", mock.Anything).Return(
		"", "", nil, req.PaymentHash, []byte("secret"), destination, int64(0), int64(0), nil, nil, nil,
	)
	client.On("GetPeerId", mock.Anything).Return(otherPeer, nil)

	i := newCharacterizationInterceptor(store, client, nil)
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_RESUME, result.Action)
}

// TestIntercept_IsConnectedError_FailsTemporaryChannelFailure: if flspd
// can't even determine whether the next hop is connected, fail the HTLC —
// there's no safe default to resume into for a registered payment.
func TestIntercept_IsConnectedError_FailsTemporaryChannelFailure(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	req := testInterceptRequest()

	destination := fakePubkey(0x01)
	store.On("PaymentInfo", mock.Anything).Return(
		"", "", nil, req.PaymentHash, []byte("secret"), destination, int64(0), int64(0), nil, nil, nil,
	)
	client.On("GetPeerId", mock.Anything).Return(nil, nil) // unknown scid -> falls back to destination
	client.On("IsConnected", mock.Anything).Return(false, assert.AnError)

	i := newCharacterizationInterceptor(store, client, nil)
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_FAIL_HTLC_WITH_CODE, result.Action)
	assert.Equal(t, core.FAILURE_TEMPORARY_CHANNEL_FAILURE, result.FailureCode)
}

// TestIntercept_ProbeDetected_PeerNotConnected_Resumes: a probe (per the
// probing-01 convention: registered paymentHash differs from the HTLC's
// actual hash) whose peer isn't connected is resumed — probing an
// offline client isn't actionable.
func TestIntercept_ProbeDetected_PeerNotConnected_Resumes(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	req := testInterceptRequest()

	destination := fakePubkey(0x01)
	registeredHash := []byte{0xcc, 0xdd} // differs from req.PaymentHash -> probe
	store.On("PaymentInfo", mock.Anything).Return(
		"", "", nil, registeredHash, []byte("secret"), destination, int64(0), int64(0), nil, nil, nil,
	)
	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(false, nil)

	i := newCharacterizationInterceptor(store, client, nil)
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_RESUME, result.Action)
}

// TestIntercept_ProbeDetected_PeerConnected_NoChannel_FailsIncorrectPaymentDetails:
// a probe whose peer IS connected but has no channel yet gets a deliberate
// out-of-spec failure code (INCORRECT_OR_UNKNOWN_PAYMENT_DETAILS) — a hint
// to probing-01-aware senders that the real payment would likely succeed,
// per the inline comment on this branch in intercept_handler.go.
func TestIntercept_ProbeDetected_PeerConnected_NoChannel_FailsIncorrectPaymentDetails(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	notifier := new(MockNotifier)
	req := testInterceptRequest()

	destination := fakePubkey(0x01)
	registeredHash := []byte{0xcc, 0xdd}
	store.On("PaymentInfo", mock.Anything).Return(
		"", "", nil, registeredHash, []byte("secret"), destination, int64(0), int64(0), nil, nil, nil,
	)
	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything)

	i := newCharacterizationInterceptor(store, client, notifier)
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_FAIL_HTLC_WITH_CODE, result.Action)
	assert.Equal(t, core.FAILURE_INCORRECT_OR_UNKNOWN_PAYMENT_DETAILS, result.FailureCode)
}

// TestIntercept_RegisteredNonProbe_PeerConnected_NoChannelPointOrFeeParams_NonJit_TimelockOK_ProceedsToOpen
// isn't practical to assert on directly here (openChannel itself dials
// out to i.client.OpenChannel and then polls GetChannel in a loop with
// real time.Sleep — that path is exercised by openChannel's own tests in
// open_channel_test.go, not repeated here). Documented instead of tested:
// a registered, non-probe payment with a connected peer and insufficient
// CLTV headroom fails fast, before ever reaching openChannel — pinned
// below since it's a cheap, deterministic early-exit.
func TestIntercept_RegisteredNonProbe_InsufficientCltv_FailsTemporaryChannelFailure(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	notifier := new(MockNotifier)
	req := testInterceptRequest()
	// TimeLockDelta is 40 (newCharacterizationInterceptor); make the
	// incoming/outgoing expiry delta smaller than that.
	req.IncomingExpiry = 910
	req.OutgoingExpiry = 900 // delta 10 < TimeLockDelta 40

	destination := fakePubkey(0x01)
	minFee := uint64(1000)
	params := &core.OpeningFeeParams{MinFeeMloki: minFee, Proportional: 100, ValidUntil: "2099-01-01T00:00:00.000Z"}
	store.On("PaymentInfo", mock.Anything).Return(
		"", "", params, req.PaymentHash, []byte("secret"), destination, int64(0), int64(0), nil, nil, nil,
	)
	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything)

	i := newCharacterizationInterceptor(store, client, notifier)
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_FAIL_HTLC_WITH_CODE, result.Action)
	assert.Equal(t, core.FAILURE_TEMPORARY_CHANNEL_FAILURE, result.FailureCode)
}
