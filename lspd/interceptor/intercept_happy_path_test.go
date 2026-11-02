package interceptor

import (
	"context"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lspd/chain"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/history"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// fakeHistoryStore is a no-op history.Store: Intercept()'s channel-open
// success path writes to it, but none of these tests care about what was
// recorded, only that reaching that code at all doesn't panic or block.
type fakeHistoryStore struct{}

func (f *fakeHistoryStore) UpdateChannels(context.Context, []*history.ChannelUpdate) error {
	return nil
}
func (f *fakeHistoryStore) InsertForwards(context.Context, []*history.Forward, []byte) error {
	return nil
}
func (f *fakeHistoryStore) UpdateForwards(context.Context, []*history.Forward, []byte) error {
	return nil
}
func (f *fakeHistoryStore) FetchLndForwardOffset(context.Context, []byte) (*time.Time, error) {
	return nil, nil
}
func (f *fakeHistoryStore) AddOpenChannelHtlc(context.Context, *history.OpenChannelHtlc) error {
	return nil
}

// newHappyPathInterceptor extends newTestInterceptor's fixture pattern
// (open_channel_test.go) with a historyStore, needed once Intercept()'s
// flow reaches past openChannel into the GetChannel/IsChannelRoutable
// polling loops and the history-store writes on success.
func newHappyPathInterceptor(cfg *config.NodeConfig, client *MockLightningClient, store *MockStore, notifier *MockNotifier) *Interceptor {
	return &Interceptor{
		config:              config.NewNodeConfigRef(cfg),
		client:              client,
		store:               store,
		historyStore:        &fakeHistoryStore{},
		feeEstimator:        &fakeFeeEstimator{estimation: fixedFeeEstimation()},
		feeStrategy:         chain.FeeStrategyHalfHour,
		notificationService: notifier,
	}
}

// TestIntercept_RegisteredPayment_ChannelOpensSuccessfully covers the full
// happy path through Intercept() itself (as opposed to openChannel or the
// WaitChannelPointActive/IsChannelRoutable polling loops in isolation,
// already covered elsewhere): a registered payment with no existing channel
// triggers openChannel, and once the mocked client reports the new
// channel as immediately active and routable (first poll succeeds, so
// this test doesn't wait out any real ticker), Intercept must resume the
// HTLC onion with the resolved SCID/amounts.
func TestIntercept_RegisteredPayment_ChannelOpensSuccessfully(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	cfg := baseTestConfig()
	req := testInterceptRequest()
	req.IncomingExpiry = 1000
	req.OutgoingExpiry = 800 // delta 200 >= TimeLockDelta 144

	destination := fakePubkey(0x01)
	minFee := uint64(1000)
	params := &core.OpeningFeeParams{MinFeeMloki: minFee, Proportional: 0, ValidUntil: "2099-01-01T00:00:00.000Z"}
	store.On("PaymentInfo", mock.Anything).Return(
		"", "", params, req.PaymentHash, []byte("secret"), destination, int64(20_000_000), int64(19_920_000), nil, nil, nil,
	)
	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)

	outpoint := newTestOutPoint()
	client.On("OpenChannel", mock.Anything).Return(outpoint, nil)
	store.On("SetFundingTx", req.PaymentHash, outpoint).Return(nil)

	scid := lightning.ShortChannelID(12345)
	client.On("WaitChannelPointActive", destination, *outpoint, mock.Anything).Return(&lightning.GetChannelResult{ConfirmedScid: &scid}, nil)
	client.On("UpdateChannelPolicy", *outpoint, mock.Anything).Return(nil)
	client.On("IsChannelRoutable", uint64(scid)).Return(true, nil)
	client.On("IsChannelPolicyApplied", uint64(scid), destination, mock.Anything).Return(true, nil)

	notifier := new(MockNotifier)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything).Maybe()

	i := newHappyPathInterceptor(cfg, client, store, notifier)
	result := i.Intercept(req)

	require.Equal(t, core.INTERCEPT_RESUME_WITH_ONION, result.Action)
	assert.Equal(t, destination, result.Destination)
	assert.Equal(t, scid, result.Scid)
	assert.Equal(t, outpoint, result.ChannelPoint)
}

// TestIntercept_Lsps1Jit_MarksOrderFundedAndCompleted is the FLSPD-0015
// regression test: when the interceptor opens a JIT channel for a PAID LSPS1
// order (a payment persisted with lsps1JitTag), it must record the funding
// and transition the order to COMPLETED on lsps1.orders -- the LSPS1 analogue
// of the LSPS2 Consume gate. Without it the order stays PAID/PAID with
// funding_txid NULL, so GetPaidLsps1Order keeps returning it and a later HTLC
// or recovery pass opens a second channel for the same single fee.
func TestIntercept_Lsps1Jit_MarksOrderFundedAndCompleted(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	cfg := baseTestConfig() // JitEnabled stays false: no JIT business-floor gating
	req := testInterceptRequest()
	req.IncomingAmountMloki = 20_000_000
	req.IncomingExpiry = 1000
	req.OutgoingExpiry = 800

	destination := fakePubkey(0x01)
	params := &core.OpeningFeeParams{MinFeeMloki: 1000, Proportional: 0, ValidUntil: "2099-01-01T00:00:00.000Z"}
	jitTag := lsps1JitTag
	orderId := "order-lsps1-1"
	// A payment already persisted as an LSPS1 JIT redemption: PaymentInfo
	// returns its order id and the lsps1JitTag, so Intercept classifies it as
	// an LSPS1 JIT open.
	store.On("PaymentInfo", mock.Anything).Return(
		"", orderId, params, req.PaymentHash, []byte("secret"), destination, int64(20_000_000), int64(19_920_000), nil, &jitTag, nil,
	)
	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)

	outpoint := newTestOutPoint()
	client.On("OpenChannel", mock.Anything).Return(outpoint, nil)
	store.On("SetFundingTx", req.PaymentHash, outpoint).Return(nil)

	// The FLSPD-0015 fix: the order is marked funded + COMPLETED on lsps1.orders.
	store.On("UpdateLsps1OrderFunding", orderId, outpoint.Hash.String(), int(outpoint.Index)).Return(true, nil)
	store.On("UpdateLsps1OrderState", orderId, "COMPLETED", "PAID", "PAID").Return(true, nil)

	scid := lightning.ShortChannelID(12345)
	client.On("WaitChannelPointActive", destination, *outpoint, mock.Anything).Return(&lightning.GetChannelResult{ConfirmedScid: &scid}, nil)
	client.On("UpdateChannelPolicy", *outpoint, mock.Anything).Return(nil)
	client.On("IsChannelRoutable", uint64(scid)).Return(true, nil)
	client.On("IsChannelPolicyApplied", uint64(scid), destination, mock.Anything).Return(true, nil)

	notifier := new(MockNotifier)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything).Maybe()
	// The interceptor already notifies the client COMPLETED on a successful
	// JIT open; the FLSPD-0015 fix makes that notification truthful by also
	// recording COMPLETED on lsps1.orders.
	notifier.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(true, nil).Maybe()

	i := newHappyPathInterceptor(cfg, client, store, notifier)
	result := i.Intercept(req)

	require.Equal(t, core.INTERCEPT_RESUME_WITH_ONION, result.Action)
	store.AssertCalled(t, "UpdateLsps1OrderFunding", orderId, outpoint.Hash.String(), int(outpoint.Index))
	store.AssertCalled(t, "UpdateLsps1OrderState", orderId, "COMPLETED", "PAID", "PAID")
}

// TestIntercept_RegisteredPayment_OpenChannelFails covers the error path
// immediately after openChannel is invoked from within Intercept, as
// opposed to openChannel's own error-propagation tests.
func TestIntercept_RegisteredPayment_OpenChannelFails(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	cfg := baseTestConfig()
	req := testInterceptRequest()
	req.IncomingExpiry = 1000
	req.OutgoingExpiry = 800

	destination := fakePubkey(0x01)
	params := &core.OpeningFeeParams{MinFeeMloki: 1000, Proportional: 0, ValidUntil: "2099-01-01T00:00:00.000Z"}
	store.On("PaymentInfo", mock.Anything).Return(
		"", "", params, req.PaymentHash, []byte("secret"), destination, int64(20_000_000), int64(19_920_000), nil, nil, nil,
	)
	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)
	client.On("OpenChannel", mock.Anything).Return(nil, assert.AnError)

	notifier := new(MockNotifier)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything).Maybe()

	i := newHappyPathInterceptor(cfg, client, store, notifier)
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_FAIL_HTLC_WITH_CODE, result.Action)
	assert.Equal(t, core.FAILURE_TEMPORARY_CHANNEL_FAILURE, result.FailureCode)
}

// TestIntercept_RegisteredPayment_ExpiredFeeParams_ChainFeeNotCheaper
// covers the opening_fee_params-expiry guard: an expired quote must fail
// the HTLC rather than silently honoring stale pricing, unless the
// current chain fee is cheaper (see the sibling test below).
func TestIntercept_RegisteredPayment_ExpiredFeeParams_ChainFeeNotCheaper(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	cfg := baseTestConfig()
	req := testInterceptRequest()
	req.IncomingExpiry = 1000
	req.OutgoingExpiry = 800

	destination := fakePubkey(0x01)
	params := &core.OpeningFeeParams{MinFeeMloki: 1000, Proportional: 0, ValidUntil: "2000-01-01T00:00:00.000Z"} // long expired
	store.On("PaymentInfo", mock.Anything).Return(
		"", "", params, req.PaymentHash, []byte("secret"), destination, int64(20_000_000), int64(19_920_000), nil, nil, nil,
	)
	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)

	notifier := new(MockNotifier)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything).Maybe()

	i := newHappyPathInterceptor(cfg, client, store, notifier)
	i.openingService = &fakeOpeningServiceChainFee{cheaper: false}
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_FAIL_HTLC_WITH_CODE, result.Action)
	assert.Equal(t, core.FAILURE_TEMPORARY_CHANNEL_FAILURE, result.FailureCode)
	client.AssertNotCalled(t, "OpenChannel", mock.Anything)
}

// fakeOpeningServiceChainFee lets tests control IsCurrentChainFeeCheaper's
// answer directly, without needing a full ports.OpeningService mock for
// the one method Intercept's expired-fee-params path calls. Embedding the
// real interface (left nil) means any other method would nil-panic if
// called -- acceptable here since these tests never reach one.
type fakeOpeningServiceChainFee struct {
	ports.OpeningService
	cheaper bool
}

func (f *fakeOpeningServiceChainFee) IsCurrentChainFeeCheaper(currentFeeratePerVByte *float64, referenceFeeratePerVByte float64) bool {
	return f.cheaper
}
