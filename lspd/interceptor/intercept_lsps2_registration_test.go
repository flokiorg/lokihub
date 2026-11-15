package interceptor

import (
	"testing"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// lsps2RegisteredRequest builds an InterceptRequest/mock-store combination
// that drives resolvePayment through its LSPS2 buy_registrations fallback
// (not PaymentInfo's direct-hit branch), so isLsps2Registration is true and
// Intercept's Claim/Consume/Release calls actually fire.
func lsps2RegisteredRequest(store *MockStore, destination []byte, scid lightning.ShortChannelID) core.InterceptRequest {
	req := testInterceptRequest()
	req.Scid = scid
	req.IncomingExpiry = 1000
	req.OutgoingExpiry = 800 // delta 200 >= TimeLockDelta 40
	// Large enough (in mloki) that the resulting loki capacity clears
	// baseTestConfig's network floor once openChannel computes it --
	// matches the scale intercept_happy_path_test.go's fixtures already use.
	req.IncomingAmountMloki = 20_000_000

	// PaymentInfo: not found (forces the LSPS2 fallback in resolvePayment).
	store.On("PaymentInfo", mock.Anything).Return("", "", (*core.OpeningFeeParams)(nil), []byte(nil), []byte(nil), []byte(nil), int64(0), int64(0), (*wire.OutPoint)(nil), (*string)(nil), nil)

	paymentSize := uint64(20_000_000) // <= req.IncomingAmountMloki
	store.On("GetLsps2Order", uint64(scid)).Return(&core.Lsps2BuyRegistration{
		Id:                 "reg-1",
		PeerId:             hexEncode(destination),
		Scid:               uint64(scid),
		PaymentSizeMloki:   &paymentSize,
		ParamsMinFeeMloki:  1000,
		ParamsProportional: 0,
		ParamsValidUntil:   "2099-01-01T00:00:00.000Z",
	}, nil)
	store.On("RegisterPayment", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	return req
}

func hexEncode(b []byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hextable[v>>4]
		out[i*2+1] = hextable[v&0x0f]
	}
	return string(out)
}

// TestIntercept_F15_ClaimFailure_PreventsSecondChannelOpen is THE
// regression test for F-15: when a registration is no longer claimable
// (already claimed by a concurrent/prior HTLC, already consumed, or
// expired), Intercept must NOT call openChannel a second time. Before this
// fix, nothing checked this at all -- a second, distinct-payment-hash HTLC
// through the same phantom SCID would open a second real channel.
func TestIntercept_F15_ClaimFailure_PreventsSecondChannelOpen(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	notifier := new(MockNotifier)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything).Maybe()
	notifier.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(true, nil).Maybe()

	destination := fakePubkey(0x02)
	scid := lightning.ShortChannelID(999)
	req := lsps2RegisteredRequest(store, destination, scid)

	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)

	// The registration is no longer claimable -- e.g. a concurrent HTLC
	// already won the race, or it was already consumed/expired.
	store.On("ClaimLsps2Registration", uint64(scid)).Return(false, nil)

	i := newHappyPathInterceptor(baseTestConfig(), client, store, notifier)
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_FAIL_HTLC_WITH_CODE, result.Action)
	assert.Equal(t, core.FAILURE_TEMPORARY_CHANNEL_FAILURE, result.FailureCode)
	client.AssertNotCalled(t, "OpenChannel", mock.Anything)
	store.AssertNotCalled(t, "ConsumeLsps2Registration", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestIntercept_F15_SuccessfulOpen_ConsumesRegistration proves the happy
// path still works end-to-end with the new Claim/Consume lifecycle, and
// that Consume is called with the real channel point once the channel is
// confirmed open.
func TestIntercept_F15_SuccessfulOpen_ConsumesRegistration(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	notifier := new(MockNotifier)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything).Maybe()
	notifier.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(true, nil).Maybe()

	destination := fakePubkey(0x03)
	scid := lightning.ShortChannelID(1001)
	req := lsps2RegisteredRequest(store, destination, scid)

	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)
	store.On("ClaimLsps2Registration", uint64(scid)).Return(true, nil)

	outpoint := newTestOutPoint()
	client.On("OpenChannel", mock.Anything).Return(outpoint, nil)
	store.On("SetFundingTx", mock.Anything, outpoint).Return(nil)

	confirmedScid := lightning.ShortChannelID(55555)
	client.On("WaitChannelPointActive", mock.Anything, *outpoint, mock.Anything).Return(&lightning.GetChannelResult{ConfirmedScid: &confirmedScid}, nil)
	client.On("UpdateChannelPolicy", *outpoint, mock.Anything).Return(nil)
	client.On("IsChannelRoutable", uint64(confirmedScid)).Return(true, nil)
	client.On("IsChannelPolicyApplied", uint64(confirmedScid), mock.Anything, mock.Anything).Return(true, nil)

	store.On("ConsumeLsps2Registration", uint64(scid), outpoint, mock.Anything, mock.Anything).Return(true, nil)

	i := newHappyPathInterceptor(baseTestConfig(), client, store, notifier)
	result := i.Intercept(req)

	require.Equal(t, core.INTERCEPT_RESUME_WITH_ONION, result.Action)
	store.AssertCalled(t, "ConsumeLsps2Registration", uint64(scid), outpoint, mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "ReleaseLsps2Registration", mock.Anything)
}

// TestIntercept_F15_OpenChannelFailure_ReleasesRegistrationForRetry proves
// a failed channel-open doesn't permanently strand the registration at
// OPENING -- it must be released back to ACTIVE so a later HTLC can retry.
func TestIntercept_F15_OpenChannelFailure_ReleasesRegistrationForRetry(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	notifier := new(MockNotifier)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything).Maybe()
	notifier.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(true, nil).Maybe()

	destination := fakePubkey(0x04)
	scid := lightning.ShortChannelID(1002)
	req := lsps2RegisteredRequest(store, destination, scid)

	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)
	store.On("ClaimLsps2Registration", uint64(scid)).Return(true, nil)
	client.On("OpenChannel", mock.Anything).Return(nil, assert.AnError)
	store.On("ReleaseLsps2Registration", uint64(scid)).Return(nil)

	i := newHappyPathInterceptor(baseTestConfig(), client, store, notifier)
	result := i.Intercept(req)

	assert.Equal(t, core.INTERCEPT_FAIL_HTLC_WITH_CODE, result.Action)
	store.AssertCalled(t, "ReleaseLsps2Registration", uint64(scid))
	store.AssertNotCalled(t, "ConsumeLsps2Registration", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestIntercept_F15_RetryAfterReleaseStillEnforcesClaimGate is the
// regression test for the retry-classification bug: resolvePayment used
// to set isJit/isLsps2Registration ONLY inside its "PaymentInfo not
// found" fallback, so once RegisterPayment had written a row for this
// payment_hash (as it does on the very first Intercept call, win or
// lose), any LATER Intercept call for that same hash -- e.g. a realistic
// sender retry after openChannel failed and ReleaseLsps2Registration
// already put the registration back to ACTIVE -- would hit PaymentInfo's
// "found" branch and silently lose the classification, skipping
// ClaimLsps2Registration entirely on the retry. This test drives exactly
// that sequence: first call fails cleanly (releases), second call (the
// retry) must still call ClaimLsps2Registration before attempting
// openChannel again, and must still call ConsumeLsps2Registration once it
// succeeds.
func TestIntercept_F15_RetryAfterReleaseStillEnforcesClaimGate(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	notifier := new(MockNotifier)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything).Maybe()
	notifier.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(true, nil).Maybe()

	destination := fakePubkey(0x06)
	scid := lightning.ShortChannelID(1004)
	req := testInterceptRequest()
	req.Scid = scid
	req.IncomingExpiry = 1000
	req.OutgoingExpiry = 800
	req.IncomingAmountMloki = 20_000_000
	paymentHash := req.PaymentHash

	// FIRST call only: PaymentInfo has no row yet, forcing resolvePayment's
	// LSPS2 fallback.
	store.On("PaymentInfo", mock.Anything).Return("", "", (*core.OpeningFeeParams)(nil), []byte(nil), []byte(nil), []byte(nil), int64(0), int64(0), (*wire.OutPoint)(nil), (*string)(nil), nil).Once()

	paymentSize := uint64(20_000_000)
	store.On("GetLsps2Order", uint64(scid)).Return(&core.Lsps2BuyRegistration{
		Id:                 "reg-retry",
		PeerId:             hexEncode(destination),
		Scid:               uint64(scid),
		PaymentSizeMloki:   &paymentSize,
		ParamsMinFeeMloki:  1000,
		ParamsProportional: 0,
		ParamsValidUntil:   "2099-01-01T00:00:00.000Z",
	}, nil)
	store.On("RegisterPayment", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, lsps2JitTag).Return(nil)

	// SECOND call onward: PaymentInfo now finds the row the first call's
	// RegisterPayment wrote -- a real, non-empty payment_hash/token, the
	// persisted lsps2JitTag, and channelPoint still nil (the first
	// attempt's openChannel never reached SetFundingTx). This is exactly
	// what a real retry looks like.
	retryTag := lsps2JitTag
	store.On("PaymentInfo", mock.Anything).Return(
		"token-1", "reg-retry",
		&core.OpeningFeeParams{MinFeeMloki: 1000, ValidUntil: "2099-01-01T00:00:00.000Z"},
		paymentHash, []byte("lsps2-jit"), destination,
		int64(20_000_000), int64(20_000_000),
		(*wire.OutPoint)(nil), &retryTag, nil,
	)

	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)

	// First call: claim succeeds, but the on-chain open itself fails
	// cleanly (no channel exists at all) -- Release runs.
	store.On("ClaimLsps2Registration", uint64(scid)).Return(true, nil).Once()
	client.On("OpenChannel", mock.Anything).Return(nil, assert.AnError).Once()
	store.On("ReleaseLsps2Registration", uint64(scid)).Return(nil).Once()

	i := newHappyPathInterceptor(baseTestConfig(), client, store, notifier)
	firstResult := i.Intercept(req)
	assert.Equal(t, core.INTERCEPT_FAIL_HTLC_WITH_CODE, firstResult.Action)
	store.AssertCalled(t, "ReleaseLsps2Registration", uint64(scid))

	// Second call (the retry): must ALSO claim -- proving
	// isLsps2Registration/isJit were correctly re-derived from the
	// persisted tag on the "found" branch, not silently lost. This time
	// the open succeeds all the way through.
	store.On("ClaimLsps2Registration", uint64(scid)).Return(true, nil).Once()
	outpoint := newTestOutPoint()
	client.On("OpenChannel", mock.Anything).Return(outpoint, nil).Once()
	store.On("SetFundingTx", mock.Anything, outpoint).Return(nil)
	confirmedScid := lightning.ShortChannelID(99999)
	client.On("WaitChannelPointActive", mock.Anything, *outpoint, mock.Anything).Return(&lightning.GetChannelResult{ConfirmedScid: &confirmedScid}, nil)
	client.On("UpdateChannelPolicy", *outpoint, mock.Anything).Return(nil)
	client.On("IsChannelRoutable", uint64(confirmedScid)).Return(true, nil)
	client.On("IsChannelPolicyApplied", uint64(confirmedScid), mock.Anything, mock.Anything).Return(true, nil)
	store.On("ConsumeLsps2Registration", uint64(scid), outpoint, mock.Anything, mock.Anything).Return(true, nil)

	secondResult := i.Intercept(req)

	store.AssertNumberOfCalls(t, "ClaimLsps2Registration", 2)
	require.Equal(t, core.INTERCEPT_RESUME_WITH_ONION, secondResult.Action)
	store.AssertCalled(t, "ConsumeLsps2Registration", uint64(scid), outpoint, mock.Anything, mock.Anything)
}

// TestResolvePayment_F22_VariableSizeMode_RejectsAmountAboveRegistrationBound
// is the regression test for F-22's redemption-time half: a registration's
// own persisted worst-case bound (MaxPaymentSizeMloki) must be enforced
// against the REAL incoming HTLC amount in variable-size mode -- before
// this fix, nothing checked this at all, so a channel could be opened
// larger than what the buy-time reservation (ReservedAmountLoki) ever
// accounted for.
func TestResolvePayment_F22_VariableSizeMode_RejectsAmountAboveRegistrationBound(t *testing.T) {
	store := new(MockStore)
	i := &Interceptor{store: store}

	scid := uint64(2001)
	maxPaymentSize := uint64(20_000_000)
	store.On("PaymentInfo", mock.Anything).Return("", "", (*core.OpeningFeeParams)(nil), []byte(nil), []byte(nil), []byte(nil), int64(0), int64(0), (*wire.OutPoint)(nil), (*string)(nil), nil)
	store.On("GetLsps2Order", scid).Return(&core.Lsps2BuyRegistration{
		Id:                  "reg-bound",
		PeerId:              "02deadbeef",
		Scid:                scid,
		PaymentSizeMloki:    nil, // variable-size mode
		MaxPaymentSizeMloki: &maxPaymentSize,
		ParamsMinFeeMloki:   1000,
		ParamsValidUntil:    "2099-01-01T00:00:00.000Z",
	}, nil)

	req := core.InterceptRequest{
		Scid:                lightning.ShortChannelID(scid),
		IncomingAmountMloki: 30_000_000, // exceeds the registration's own 20_000_000 bound
		PaymentHash:         []byte("hash"),
	}

	result, err := i.resolvePayment(req)

	require.Error(t, err, "an incoming amount above the registration's own worst-case bound must be rejected")
	assert.Nil(t, result)
	store.AssertNotCalled(t, "RegisterPayment", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestResolvePayment_F22_VariableSizeMode_AllowsAmountWithinRegistrationBound
// proves the bound doesn't false-positive-reject a legitimate payment at
// or below the registration's own worst-case figure.
func TestResolvePayment_F22_VariableSizeMode_AllowsAmountWithinRegistrationBound(t *testing.T) {
	store := new(MockStore)
	i := &Interceptor{store: store}

	scid := uint64(2002)
	maxPaymentSize := uint64(20_000_000)
	store.On("PaymentInfo", mock.Anything).Return("", "", (*core.OpeningFeeParams)(nil), []byte(nil), []byte(nil), []byte(nil), int64(0), int64(0), (*wire.OutPoint)(nil), (*string)(nil), nil)
	store.On("GetLsps2Order", scid).Return(&core.Lsps2BuyRegistration{
		Id:                  "reg-bound-ok",
		PeerId:              "02deadbeef",
		Scid:                scid,
		PaymentSizeMloki:    nil,
		MaxPaymentSizeMloki: &maxPaymentSize,
		ParamsMinFeeMloki:   1000,
		ParamsValidUntil:    "2099-01-01T00:00:00.000Z",
	}, nil)
	store.On("RegisterPayment", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	req := core.InterceptRequest{
		Scid:                lightning.ShortChannelID(scid),
		IncomingAmountMloki: 20_000_000, // exactly at the bound
		PaymentHash:         []byte("hash"),
	}

	result, err := i.resolvePayment(req)

	require.NoError(t, err)
	require.NotNil(t, result)
}

// TestIntercept_PartialOpenSuccess_ChannelOpenedButFundingWriteFails_NotReleased
// is the regression test for the "partial success" bug: openChannel can
// return a non-nil channelPoint (the on-chain OpenChannel call itself
// succeeded, real capital committed) together with a non-nil error (the
// subsequent SetFundingTx DB write failed). Intercept used to check only
// `err != nil` and treat this exactly like a total failure -- releasing
// the registration back to ACTIVE even though a real, funded channel
// already exists for it, which would let a later HTLC claim it again and
// open a SECOND real channel. Must instead be treated as consumed, not
// failed, and the flow must continue through to a normal successful
// result using the channelPoint already in hand.
func TestIntercept_PartialOpenSuccess_ChannelOpenedButFundingWriteFails_NotReleased(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	notifier := new(MockNotifier)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything).Maybe()
	notifier.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(true, nil).Maybe()

	destination := fakePubkey(0x07)
	scid := lightning.ShortChannelID(1005)
	req := lsps2RegisteredRequest(store, destination, scid)

	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)
	store.On("ClaimLsps2Registration", uint64(scid)).Return(true, nil)

	outpoint := newTestOutPoint()
	client.On("OpenChannel", mock.Anything).Return(outpoint, nil)
	// The on-chain open succeeds, but persisting the funding record fails.
	store.On("SetFundingTx", mock.Anything, outpoint).Return(assert.AnError)

	confirmedScid := lightning.ShortChannelID(55556)
	client.On("WaitChannelPointActive", mock.Anything, *outpoint, mock.Anything).Return(&lightning.GetChannelResult{ConfirmedScid: &confirmedScid}, nil)
	client.On("UpdateChannelPolicy", *outpoint, mock.Anything).Return(nil)
	client.On("IsChannelRoutable", uint64(confirmedScid)).Return(true, nil)
	client.On("IsChannelPolicyApplied", uint64(confirmedScid), mock.Anything, mock.Anything).Return(true, nil)

	store.On("ConsumeLsps2Registration", uint64(scid), outpoint, mock.Anything, mock.Anything).Return(true, nil)

	i := newHappyPathInterceptor(baseTestConfig(), client, store, notifier)
	result := i.Intercept(req)

	require.Equal(t, core.INTERCEPT_RESUME_WITH_ONION, result.Action, "a partial-success open must still resolve the HTLC, not fail it")
	store.AssertCalled(t, "ConsumeLsps2Registration", uint64(scid), outpoint, mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "ReleaseLsps2Registration", mock.Anything)
}

// TestIntercept_F22_NoFixedPaymentSize_StillForwardsRealAmount is the
// interceptor-side regression test for F-22's redemption-time half: a
// registration with no PaymentSizeMloki (BLIP-52's legal variable-size
// mode) must still compute a non-zero forward amount from the real
// incoming payment -- before this fix, outgoingAmountMloki stayed at its
// zero default for the whole rest of the flow, guaranteeing the forwarded
// HTLC would fail even after a real channel was opened.
func TestIntercept_F22_NoFixedPaymentSize_StillForwardsRealAmount(t *testing.T) {
	store := new(MockStore)
	client := new(MockLightningClient)
	notifier := new(MockNotifier)
	notifier.On("NotifyAsync", mock.Anything, mock.Anything, mock.Anything).Maybe()
	notifier.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(true, nil).Maybe()

	destination := fakePubkey(0x05)
	scid := lightning.ShortChannelID(1003)
	req := testInterceptRequest()
	req.Scid = scid
	req.IncomingExpiry = 1000
	req.OutgoingExpiry = 800
	req.IncomingAmountMloki = 20_000_000

	store.On("PaymentInfo", mock.Anything).Return("", "", (*core.OpeningFeeParams)(nil), []byte(nil), []byte(nil), []byte(nil), int64(0), int64(0), (*wire.OutPoint)(nil), (*string)(nil), nil)
	// No PaymentSizeMloki set -- variable-size mode.
	store.On("GetLsps2Order", uint64(scid)).Return(&core.Lsps2BuyRegistration{
		Id:                 "reg-variable",
		PeerId:             hexEncode(destination),
		Scid:               uint64(scid),
		PaymentSizeMloki:   nil,
		ParamsMinFeeMloki:  1000,
		ParamsProportional: 0,
		ParamsValidUntil:   "2099-01-01T00:00:00.000Z",
	}, nil)
	store.On("RegisterPayment", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	client.On("GetPeerId", mock.Anything).Return(nil, nil)
	client.On("IsConnected", mock.Anything).Return(true, nil)
	store.On("ClaimLsps2Registration", uint64(scid)).Return(true, nil)

	outpoint := newTestOutPoint()
	client.On("OpenChannel", mock.Anything).Return(outpoint, nil)
	store.On("SetFundingTx", mock.Anything, outpoint).Return(nil)
	confirmedScid := lightning.ShortChannelID(66666)
	client.On("WaitChannelPointActive", mock.Anything, *outpoint, mock.Anything).Return(&lightning.GetChannelResult{ConfirmedScid: &confirmedScid}, nil)
	client.On("UpdateChannelPolicy", *outpoint, mock.Anything).Return(nil)
	client.On("IsChannelRoutable", uint64(confirmedScid)).Return(true, nil)
	client.On("IsChannelPolicyApplied", uint64(confirmedScid), mock.Anything, mock.Anything).Return(true, nil)
	store.On("ConsumeLsps2Registration", uint64(scid), outpoint, mock.Anything, mock.Anything).Return(true, nil)

	i := newHappyPathInterceptor(baseTestConfig(), client, store, notifier)
	result := i.Intercept(req)

	require.Equal(t, core.INTERCEPT_RESUME_WITH_ONION, result.Action)
	assert.Greater(t, result.AmountMloki, uint64(0), "F-22: forward amount must be computed from the real incoming payment, not left at zero, when PaymentSizeMloki is nil")
}
