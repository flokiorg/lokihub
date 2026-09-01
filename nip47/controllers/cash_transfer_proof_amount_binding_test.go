package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// TestCashTransfer_ProofBoundToAmount_ReplayWithDifferentAmountRejected is a
// regression test for a critical fund-theft bug found during the 2026-07-30
// Cash Hub security audit: a kind-35521 cash_transfer proof was not bound to
// amount_millis and was not single-use, contrary to NIP-CASH §Transferring
// and Splitting a Slice ("a proof MUST NOT be replayable to authorize a
// DIFFERENT amount_millis than the one it was signed for") and §Processing
// Algorithm step 1.
//
// Attacker model: any holder of the (deliberately-shared) cash_wallet
// connection who is the counterparty of a victim's partial split — i.e. the
// victim transfers a small amount to the attacker's own identity. No victim
// private key is needed; the attacker only replays the victim's own
// already-broadcast, still-fresh proof with a larger (or omitted = full)
// amount. Before the fix, this succeeded and drained the victim's entire
// remaining change to the attacker; now it's rejected.
func TestCashTransfer_ProofBoundToAmount_ReplayWithDifferentAmountRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, 500_000, tests.RandomHex32())

	// Victim owns a 5000-mloki slice.
	victimPrivkey := nostr.GeneratePrivateKey()
	victimPubkey, _ := nostr.GetPublicKey(victimPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: victimPubkey, AmountMloki: 5000},
	}))

	attackerPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// The victim signs a proof bound to (wallet, attackerPubkey, 2000). An
	// attacker who captured it tries to use it for a DIFFERENT amount (3000).
	// The amount-binding in the proof (verifyTransferIdentityEvent checks the
	// signed amount_millis tag against the resolved amount) rejects it before any
	// slice is claimed or any funds move — no fund movement needed to exercise
	// the guard.
	proof := buildTransferProofEvent(t, victimPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, attackerPubkey, "", 2000, nil, time.Now())
	mismatched := uint64(3000)
	resp := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: victimPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: attackerPubkey},
		AmountMillis:  &mismatched,
	})
	require.NotNil(t, resp.Error, "a proof signed for amount_millis=2000 must not authorize a transfer of 3000")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)

	// The victim's slice must be completely untouched: not claimed, still 5000.
	victimClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, victimPubkey)
	require.NoError(t, err)
	require.NotNil(t, victimClaim)
	assert.Nil(t, victimClaim.ClaimedAt)
	assert.Equal(t, int64(5000), victimClaim.AmountMloki)
}

// TestCashTransfer_ProofSingleUse_ExactReplayRejected is the companion
// regression test: even a replay for the EXACT SAME amount_millis the proof
// was signed for must be rejected the second time. Amount-binding alone
// doesn't stop this — a partial split leaves the source slice's registered
// identity unchanged (unlike an in-place full reassignment, which
// self-invalidates a replayed proof by changing the identity a proof must
// match), so without an explicit single-use guard a captured proof could be
// resubmitted repeatedly within its freshness window to carve off the same
// amount multiple times, each time up to the slice's live remaining balance
// — netting the attacker more than the victim ever authorized.
func TestCashTransfer_ProofSingleUse_ExactReplayRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, 500_000, tests.RandomHex32())
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	// The legitimate first split funds two internal transfers (carved +
	// remainder), so two distinct-payment-hash invoices.
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-carved", Amount: 2000},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, PaymentHash: tests.MockLNClientHoldTransaction.PaymentHash, Preimage: "preimage-remainder", Amount: 3000},
	}

	victimPrivkey := nostr.GeneratePrivateKey()
	victimPubkey, _ := nostr.GetPublicKey(victimPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: victimPubkey, AmountMloki: 5000},
	}))

	attackerPrivkey := nostr.GeneratePrivateKey()
	attackerPubkey, _ := nostr.GetPublicKey(attackerPrivkey)

	proof := buildTransferProofEvent(t, victimPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, attackerPubkey, "", 2000, nil, time.Now())
	amount := uint64(2000)

	resp1 := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: victimPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: attackerPubkey},
		AmountMillis:  &amount,
	})
	require.Nil(t, resp1.Error)

	// Replay the identical proof for the identical amount. Under the two-wallet
	// model the first split consumed the victim's source slice terminally, so
	// there is nothing left to carve off a second time — the replay finds no
	// slice for the victim's identity and is rejected. (This is a STRONGER
	// protection than the old single-use guard: the source is gone, not merely
	// marked used.)
	resp2 := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: victimPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: attackerPubkey},
		AmountMillis:  &amount,
	})
	require.NotNil(t, resp2.Error, "a replayed proof must not authorize a second carve-off")

	// The victim's source slice is terminal (consumed by the first split), not
	// left with a re-splittable balance.
	src := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, victimPubkey)
	require.NotNil(t, src)
	require.NotNil(t, src.ClaimedAt, "the source slice must be consumed once, never re-splittable")
}
