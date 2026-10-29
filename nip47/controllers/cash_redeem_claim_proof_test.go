package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flokiorg/lokihub/decodepay"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// redeemFixture is one funded bill with one pubkey slice of 1000 millis.
type redeemFixture struct {
	svc    *tests.TestService
	hub    *db.App
	wallet *db.App
	priv   string
	pub    string
}

func newRedeemFixture(t *testing.T) *redeemFixture {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	priv := nostr.GeneratePrivateKey()
	pub, _ := nostr.GetPublicKey(priv)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pub, AmountMloki: 1000},
	}))
	return &redeemFixture{svc: svc, hub: hub, wallet: wallet, priv: priv, pub: pub}
}

// params is a redeem of the whole slice to the amountless mock invoice, with a
// fresh claim proof.
func (f *redeemFixture) params(t *testing.T) nipcash.CashRedeemRequest {
	proof := buildClaimProofEvent(t, f.priv, *f.wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	return nipcash.CashRedeemRequest{
		Invoice: tests.MockZeroAmountInvoice, Amount: ptrUint64(1000),
		IdentityType: db.CashIdentityPubkey, IdentityValue: f.pub, IdentityEvent: mustMarshal(t, proof),
	}
}

func (f *redeemFixture) slice(t *testing.T) *db.CashWalletClaim {
	return cashWalletClaimByIdentity(t, f.svc, f.wallet.ID, db.CashIdentityPubkey, f.pub)
}

func (f *redeemFixture) consumedProofs(t *testing.T) int64 {
	var n int64
	require.NoError(t, f.svc.DB.Model(&db.CashConsumedProof{}).Count(&n).Error)
	return n
}

// The claim proof is consumed in the claim's own transaction: a successful
// redeem leaves it used.
func TestCashRedeem_SuccessConsumesTheClaimProof(t *testing.T) {
	f := newRedeemFixture(t)
	params := f.params(t)
	resp := handleClaimFundsFor(t, f.svc, NewTestNip47Controller(f.svc), f.wallet, params)
	require.Nil(t, resp.Error, "%+v", resp.Error)

	var consumed db.CashConsumedProof
	require.NoError(t, f.svc.DB.First(&consumed).Error)
	assert.Equal(t, constants.NIP47MethodCashRedeem, consumed.Method)
}

// U-8.5 / U-8.R3b: a payment that fails before it can have settled is
// PAYMENT_FAILED; claim and proof are released together, no cool-down is set,
// and the identical proof redeems on retry.
func TestCashRedeem_PaymentFailedReleasesClaimAndProof(t *testing.T) {
	f := newRedeemFixture(t)
	mockLn := f.svc.LNClient.(*tests.MockLn)
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, errors.New("no route"))
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, nil)

	params := f.params(t)
	controller := NewTestNip47Controller(f.svc)
	resp := handleClaimFundsFor(t, f.svc, controller, f.wallet, params)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_PAYMENT_FAILED, resp.Error.Code)

	slice := f.slice(t)
	assert.Nil(t, slice.ClaimedAt, "the slice stays claimable")
	assert.Nil(t, slice.RedeemCooldownUntil, "an immediate PAYMENT_FAILED sets no cool-down")
	assert.Zero(t, f.consumedProofs(t), "the proof of a call that never committed is not consumed")

	// The failed ledger row holds the invoice's hash, so the retry pays a fresh
	// invoice row only once the old one is failed: the identical proof works.
	retry := handleClaimFundsFor(t, f.svc, controller, f.wallet, params)
	require.Nil(t, retry.Error, "%+v", retry.Error)
}

// U-8.R3a / U-8.7: a payment still running when the reply is due is answered
// INTERNAL with the slice claimed; when it then definitely fails the claim and
// proof are released and the slice gets the cool-down, during which another
// redeem is RATE_LIMITED with nothing claimed or consumed.
func TestCashRedeem_LateFailureReleasesWithCooldown(t *testing.T) {
	f := newRedeemFixture(t)
	f.svc.Cfg.GetEnv().CashRedeemReplyWaitSecs = 1
	mockLn := f.svc.LNClient.(*tests.MockLn)
	delay := 1500 * time.Millisecond
	mockLn.PaymentDelay = &delay
	mockLn.PayInvoiceErrors = append(mockLn.PayInvoiceErrors, errors.New("no route"))
	mockLn.PayInvoiceResponses = append(mockLn.PayInvoiceResponses, nil)

	controller := NewTestNip47Controller(f.svc)
	resp := handleClaimFundsFor(t, f.svc, controller, f.wallet, f.params(t))
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_INTERNAL, resp.Error.Code)
	require.NotNil(t, f.slice(t).ClaimedAt, "claimed while the payment may still settle")
	assert.EqualValues(t, 1, f.consumedProofs(t))

	require.Eventually(t, func() bool { return f.slice(t).ClaimedAt == nil }, 5*time.Second, 50*time.Millisecond,
		"a definitely failed payout makes the slice claimable again")
	slice := f.slice(t)
	require.NotNil(t, slice.RedeemCooldownUntil)
	assert.WithinDuration(t, time.Now().Add(600*time.Second), *slice.RedeemCooldownUntil, 10*time.Second)
	assert.Zero(t, f.consumedProofs(t), "the late failure forgets the consumed proof with the claim")

	mockLn.PaymentDelay = nil
	again := handleClaimFundsFor(t, f.svc, controller, f.wallet, f.params(t))
	require.NotNil(t, again.Error)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, again.Error.Code)
	assert.Nil(t, f.slice(t).ClaimedAt)
	assert.Zero(t, f.consumedProofs(t))

	// Past the cool-down the slice redeems.
	past := time.Now().Add(-time.Second)
	require.NoError(t, f.svc.DB.Model(&db.CashWalletClaim{}).Where("id = ?", slice.ID).Update("redeem_cooldown_until", past).Error)
	ok := handleClaimFundsFor(t, f.svc, controller, f.wallet, f.params(t))
	require.Nil(t, ok.Error, "%+v", ok.Error)
}

// H-CASH-202: over the in-flight bound a redeem is RATE_LIMITED before it claims.
func TestCashRedeem_InflightBoundRefusesBeforeClaiming(t *testing.T) {
	f := newRedeemFixture(t)
	f.svc.Cfg.GetEnv().CashRedeemInflightPerHub = 1

	// Another bill of the same hub has a payout in flight.
	other := newFundedCashWallet(t, f.svc, f.hub, 500)
	require.NoError(t, f.svc.AppsService.CreateCashWalletClaims(other.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 500},
	}))
	require.NoError(t, f.svc.DB.Model(&db.CashWalletClaim{}).Where("wallet_app_id = ?", other.ID).
		Updates(map[string]interface{}{"claimed_at": time.Now(), "payment_hash": tests.RandomHex32()}).Error)

	resp := handleClaimFundsFor(t, f.svc, NewTestNip47Controller(f.svc), f.wallet, f.params(t))
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, resp.Error.Code)
	assert.Nil(t, f.slice(t).ClaimedAt)
	assert.Zero(t, f.consumedProofs(t))

	f.svc.Cfg.GetEnv().CashRedeemInflightPerHub = 2
	ok := handleClaimFundsFor(t, f.svc, NewTestNip47Controller(f.svc), f.wallet, f.params(t))
	require.Nil(t, ok.Error, "%+v", ok.Error)
}

// LR-L4: one paying node takes at most CASH_REDEEM_INFLIGHT_PER_PAYEE of a
// Hub's payouts in flight; a payout to another node still fits.
func TestCashRedeem_PerPayeeInflightBound(t *testing.T) {
	inv, err := decodepay.Decode(tests.MockZeroAmountInvoice)
	require.NoError(t, err)
	require.NotEmpty(t, inv.Payee)
	for _, c := range []struct {
		name, inFlightPayee string
		refused             bool
	}{{"same payee", strings.ToLower(inv.Payee), true}, {"other payee", "02" + tests.RandomHex32(), false}} {
		t.Run(c.name, func(t *testing.T) {
			f := newRedeemFixture(t)
			f.svc.Cfg.GetEnv().CashRedeemInflightPerPayee = 1
			other := newFundedCashWallet(t, f.svc, f.hub, 500)
			require.NoError(t, f.svc.AppsService.CreateCashWalletClaims(other.ID, []db.CashWalletClaim{
				{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 500},
			}))
			require.NoError(t, f.svc.DB.Model(&db.CashWalletClaim{}).Where("wallet_app_id = ?", other.ID).
				Updates(map[string]interface{}{"claimed_at": time.Now(), "payment_hash": tests.RandomHex32(), "redeem_payee": c.inFlightPayee}).Error)

			resp := handleClaimFundsFor(t, f.svc, NewTestNip47Controller(f.svc), f.wallet, f.params(t))
			if c.refused {
				require.NotNil(t, resp.Error)
				assert.Equal(t, constants.ERROR_RATE_LIMITED, resp.Error.Code)
				assert.Nil(t, f.slice(t).ClaimedAt)
				return
			}
			require.Nil(t, resp.Error, "%+v", resp.Error)
			assert.Equal(t, strings.ToLower(inv.Payee), f.slice(t).RedeemPayee)
		})
	}
}

// The invoice policy: an expiry or final CLTV delta over the Hub's limit is
// RESTRICTED before the claim.
func TestCashRedeem_InvoicePolicyRefusesBeforeClaiming(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(env *redeemFixture)
	}{
		{"expiry", func(f *redeemFixture) { f.svc.Cfg.GetEnv().CashRedeemMaxInvoiceExpirySecs = 86400 }},
		{"final_cltv", func(f *redeemFixture) { f.svc.Cfg.GetEnv().CashRedeemMaxFinalCLTV = 10 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newRedeemFixture(t)
			c.set(f)
			resp := handleClaimFundsFor(t, f.svc, NewTestNip47Controller(f.svc), f.wallet, f.params(t))
			require.NotNil(t, resp.Error)
			assert.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)
			assert.Nil(t, f.slice(t).ClaimedAt)
			assert.Empty(t, f.svc.LNClient.(*tests.MockLn).SendPaymentSyncCalls())
		})
	}
}

// A caller may not set the fee, the rate or same-node-ness.
func TestCashRedeem_CallerFeeFieldsRefused(t *testing.T) {
	f := newRedeemFixture(t)
	for _, field := range []string{"fee", "fees_paid", "redeem_fee_ppm", "same_node", "rate"} {
		raw, err := json.Marshal(f.params(t))
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(raw, &fields))
		fields[field] = 0
		raw, err = json.Marshal(fields)
		require.NoError(t, err)

		var resp *models.Response
		NewTestNip47Controller(f.svc).HandleCashRedeemEvent(context.TODO(),
			&models.Request{Method: constants.NIP47MethodCashRedeem, Params: raw}, 0, f.wallet,
			func(r *models.Response, _ nostr.Tags) { resp = r }, nostr.Tags{}, testPrivateCall(""))
		require.NotNil(t, resp.Error, field)
		assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code, field)
	}
	assert.Nil(t, f.slice(t).ClaimedAt)
}

// U-8.1: step order. A proof failure is answered before the invoice policy, and
// the policy before the amount rules.
func TestCashRedeem_StepOrder(t *testing.T) {
	f := newRedeemFixture(t)
	f.svc.Cfg.GetEnv().CashRedeemMaxFinalCLTV = 10

	// Bad proof and bad policy: UNAUTHORIZED.
	params := f.params(t)
	params.IdentityEvent = mustMarshal(t, buildClaimProofEvent(t, f.priv, *f.wallet.WalletPubkey, tests.RandomHex32(), nil, time.Now()))
	resp := handleClaimFundsFor(t, f.svc, NewTestNip47Controller(f.svc), f.wallet, params)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, resp.Error.Code)

	// Bad policy and bad amount: RESTRICTED.
	params = f.params(t)
	params.Amount = ptrUint64(999)
	resp = handleClaimFundsFor(t, f.svc, NewTestNip47Controller(f.svc), f.wallet, params)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)

	// Bad amount only: BAD_REQUEST.
	f.svc.Cfg.GetEnv().CashRedeemMaxFinalCLTV = 288
	resp = handleClaimFundsFor(t, f.svc, NewTestNip47Controller(f.svc), f.wallet, params)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
}
