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
// amount_mloki and was not single-use, contrary to NIP-CASH §Transferring
// and Splitting a Slice ("a proof MUST NOT be replayable to authorize a
// DIFFERENT amount_mloki than the one it was signed for") and §Processing
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
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-poc", Amount: 2000},
	}

	// Victim owns a 5000-mloki slice.
	victimPrivkey := nostr.GeneratePrivateKey()
	victimPubkey, _ := nostr.GetPublicKey(victimPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: victimPubkey, AmountMloki: 5000},
	}))

	// The attacker's own pubkey. The victim intends only to pay the attacker
	// 2000 (e.g. buying a 2000-mloki item), keeping 3000 in change.
	attackerPrivkey := nostr.GeneratePrivateKey()
	attackerPubkey, _ := nostr.GetPublicKey(attackerPrivkey)

	// The victim signs ONE proof, bound to (wallet, attackerPubkey, 2000).
	proof := buildTransferProofEvent(t, victimPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, attackerPubkey, "", 2000, nil, time.Now())

	// Step 1 — the victim's own intended partial split of 2000. Succeeds, 3000
	// remains under the victim's own identity.
	amount := uint64(2000)
	resp1 := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: victimPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: attackerPubkey},
		AmountMloki:   &amount,
	})
	require.Nil(t, resp1.Error)
	src := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, victimPubkey)
	require.NotNil(t, src)
	require.Equal(t, int64(3000), src.AmountMloki, "after the intended 2000 split, 3000 change remains for the victim")

	// Step 2 — the ATTACKER replays the SAME proof, this time as a FULL
	// transfer (amount_mloki omitted, resolving to the live 3000 remainder).
	// The proof was signed committing to amount_mloki=2000, not 3000: MUST be
	// rejected.
	resp2 := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: victimPubkey,
		IdentityEvent: mustMarshal(t, proof), // identical captured proof
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: attackerPubkey},
		// AmountMloki nil => resolves to the live 3000 remainder, which the
		// proof (bound to 2000) does not match.
	})
	require.NotNil(t, resp2.Error, "a proof signed for amount_mloki=2000 must not authorize a transfer of the live 3000 remainder")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp2.Error.Code)

	// The victim's change slice must be untouched.
	victimClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, victimPubkey)
	require.NoError(t, err)
	require.NotNil(t, victimClaim)
	assert.Equal(t, int64(3000), victimClaim.AmountMloki, "victim's change slice must survive the rejected replay untouched")
	attackerClaim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, attackerPubkey)
	require.NoError(t, err)
	assert.Nil(t, attackerClaim, "attacker must not receive the victim's change via the rejected replay")
}

// TestCashTransfer_ProofSingleUse_ExactReplayRejected is the companion
// regression test: even a replay for the EXACT SAME amount_mloki the proof
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
		AmountMloki:   &amount,
	})
	require.Nil(t, resp1.Error)

	// Replay the identical proof for the identical amount: must be rejected
	// as already-used, not treated as a second legitimate 2000 carve-off.
	resp2 := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: victimPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: attackerPubkey},
		AmountMloki:   &amount,
	})
	require.NotNil(t, resp2.Error, "an already-consumed proof must not authorize a second, identical carve-off")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp2.Error.Code)

	src := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, victimPubkey)
	require.NotNil(t, src)
	assert.Equal(t, int64(3000), src.AmountMloki, "only the first, legitimate 2000 carve-off must have taken effect")
}
