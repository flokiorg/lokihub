package controllers

// Financial audit (role A, malicious bill holder): cash_redeem accepts an
// AMOUNTLESS bolt11 (step 9 explicitly falls back to params.Amount when
// paymentRequest.AmountMloki == 0, cash_redeem_controller.go:257-260). When
// that invoice also resolves same-node, transactions.interceptSelfPayment
// settles the pre-existing incoming row with the row's OWN AmountMloki — 0 for
// an amountless invoice — while SendPaymentSync debits the caller's
// params.Amount. The slice is consumed, a preimage is returned, and the whole
// slice amount leaves the ledger without being credited anywhere.
//
// Note the existing same-node tests in this package dodge this by seeding the
// incoming row with AmountMloki: 1000. A real MakeInvoice(0, ...) records 0.
//
// bug-present: cash_redeem SUCCEEDS, slice claimed_at set, and the payee's
//              incoming row is SETTLED at 0 while 1000 was debited.
// bug-absent:  either the incoming row is credited 1000, or cash_redeem
//              refuses an amountless invoice for a same-node redemption.

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestAuditAFin_CashRedeem_AmountlessSameNodeInvoice_DestroysTheSlice(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).Pubkey = mockZeroAmountInvoicePayeePubkey

	payee, _, err := tests.CreateApp(svc)
	require.NoError(t, err)

	mockPreimage := "auditA-fin-amountless-preimage"
	require.NoError(t, svc.DB.Create(&db.Transaction{
		State:          constants.TRANSACTION_STATE_PENDING,
		Type:           constants.TRANSACTION_TYPE_INCOMING,
		PaymentRequest: tests.MockZeroAmountInvoice,
		PaymentHash:    tests.MockZeroAmountPaymentHash,
		Preimage:       &mockPreimage,
		// What MakeInvoice records for a genuinely amountless invoice.
		AmountMloki: 0,
		AppId:       &payee.ID,
	}).Error)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000, RedeemFeePpm: 100_000},
	}))

	controller := NewTestNip47Controller(svc)
	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	response := handleClaimFundsFor(t, svc, controller, wallet, nipcash.CashRedeemRequest{
		Invoice: tests.MockZeroAmountInvoice,
		// Same-node => fee-free => the required amount is the FULL slice.
		Amount:        ptrUint64(uint64(1000)),
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})
	require.Nil(t, response.Error, "the amountless same-node redemption is accepted")

	var out db.Transaction
	require.NoError(t, svc.DB.
		Where("type = ? AND payment_hash = ?", constants.TRANSACTION_TYPE_OUTGOING, tests.MockZeroAmountPaymentHash).
		First(&out).Error)
	var in db.Transaction
	require.NoError(t, svc.DB.
		Where("type = ? AND payment_hash = ?", constants.TRANSACTION_TYPE_INCOMING, tests.MockZeroAmountPaymentHash).
		First(&in).Error)

	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	consumed := claim == nil || claim.ClaimedAt != nil

	t.Logf("slice consumed=%v  debited=%d mloki  credited=%d mloki  destroyed=%d mloki",
		consumed, out.AmountMloki, in.AmountMloki, out.AmountMloki-in.AmountMloki)

	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, out.State)
	assert.EqualValues(t, 1000, out.AmountMloki, "the bill wallet is debited the full slice")
	assert.EqualValues(t, 1000, in.AmountMloki,
		"CONSERVATION: the recipient is credited %d, not 1000 — %d mloki destroyed while the slice was consumed (consumed=%v)",
		in.AmountMloki, out.AmountMloki-in.AmountMloki, consumed)
}
