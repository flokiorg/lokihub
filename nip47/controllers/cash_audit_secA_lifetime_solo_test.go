package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// TestSecA_CashInPlace_AfterCoRecipientDeleted_SpinsOffInstead is the inverted
// form of what was independent-audit-A's open finding: cash_transfer's
// "convert a slice to cash mode in place" eligibility check used to count the
// wallet's *current* claim rows (AppsService.ListClaimsForWallet) instead of the
// lifetime recipient set NIP-CASH §"Which outcome a request produces" and
// §Security Considerations both require ("evaluated against every recipient the
// wallet has EVER had, not just currently-unclaimed ones").
//
// A claim row is normally never removed for the wallet's lifetime — a redeem or
// full split sets ClaimedAt but keeps the row, so a co-recipient who redeemed and
// moved on was always counted correctly (see the paired test below). The one
// operation that DELETES a row is the admin's own DeleteCashClaim ("removing one
// bad recipient from a shared wallet"). After that, the live claim count dropped
// below the true lifetime count and the check wrongly treated a
// historically-multi-recipient wallet as lifetime-solo — allowing an in-place
// cash-mode reassignment onto a connection the removed co-recipient STILL holds.
// That secret is derived from the app ID (cashwallet/create.go:549) and so cannot
// be rotated, and a cash-mode redemption's entire proof is its raw secret in the
// request body, so the removed co-recipient could decrypt the eventual
// cash_redeem and front-run it.
//
// The fix reads the lifetime set as live rows + archived rows:
// DeleteCashClaim hard-deletes the claim and writes its archive row in the same
// transaction, so the two sets are disjoint and any archive row for the wallet
// means "a recipient was removed" (AppsService.HasArchivedSliceForWallet).
//
// This test asserts the SAFE path is now taken: the conversion spins off into a
// fresh dedicated wallet (NewWalletToken returned) and no cash-mode slice is left
// on the formerly-shared wallet.
func TestSecA_CashInPlace_AfterCoRecipientDeleted_SpinsOffInstead(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	// A wallet that HAS ALWAYS HAD two recipients (A and B). Both were handed
	// the same shared connection at creation. Funded generously, with a queued
	// mock invoice, so the spin-off path's internal funding transfer can now
	// actually succeed — the old in-place path needed neither.
	wallet := newFundedCashWallet(t, svc, hub, 200_000)
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-secA-spinoff", Amount: 1000},
	}

	aPrivkey := nostr.GeneratePrivateKey()
	aPubkey, _ := nostr.GetPublicKey(aPrivkey)
	bPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: aPubkey, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: bPubkey, AmountMloki: 1000},
	}))

	// Sanity: the wallet's lifetime recipient count is 2 right now.
	claimsBefore, err := svc.AppsService.ListClaimsForWallet(wallet.ID)
	require.NoError(t, err)
	require.Len(t, claimsBefore, 2)

	// The hub owner removes co-recipient B's still-unclaimed slice — a routine,
	// documented admin action. This DELETES B's claim row; B nonetheless still
	// holds the (unchanged, underivable-otherwise) shared connection secret.
	bClaim := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, bPubkey)
	_, err = svc.AppsService.DeleteCashClaim(wallet.ID, bClaim.ID)
	require.NoError(t, err)

	claimsAfter, err := svc.AppsService.ListClaimsForWallet(wallet.ID)
	require.NoError(t, err)
	require.Len(t, claimsAfter, 1, "the live count alone has dropped below the true lifetime count")

	// ...but the archive row DeleteCashClaim wrote in the same transaction is
	// what makes the lifetime count recoverable. This is the signal the fix reads.
	hadRemoved, err := svc.AppsService.HasArchivedSliceForWallet(wallet.ID)
	require.NoError(t, err)
	require.True(t, hadRemoved, "DeleteCashClaim must leave an archive row, or the lifetime count is unrecoverable")

	// A converts their slice to cash mode via a FULL transfer.
	_, newSecretHash := cashSecretAndHash(t)
	proof := buildTransferProofEvent(t, aPrivkey, *wallet.WalletPubkey, db.CashIdentityCash, newSecretHash, "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: aPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error)
	result, ok := response.Result.(cashTransferResponse)
	require.True(t, ok, "unexpected result type %T", response.Result)

	// THE FIX: the cash note is spun off into a fresh, never-shared dedicated
	// wallet rather than reassigned in place onto the connection B still holds.
	assert.NotEmpty(t, result.NewWalletToken,
		"SECURITY: a wallet that ever had a co-recipient removed MUST spin off, not reassign in place")
	// Queried directly rather than via cashWalletClaimByIdentity, which
	// require.NoError's on a missing row and so cannot express "must be absent".
	var leftBehind int64
	require.NoError(t, svc.DB.Model(&db.CashWalletClaim{}).
		Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ?",
			wallet.ID, db.CashIdentityCash, newSecretHash).
		Count(&leftBehind).Error)
	assert.Zero(t, leftBehind,
		"SECURITY: no cash-mode slice may be left on the formerly-shared wallet — B could decrypt its cash_redeem and steal the secret")
}

// TestSecA_CashInPlace_RedeemedCoRecipientStillCounted is the paired
// CONFIRMED-SAFE case: a co-recipient who REDEEMED (rather than being deleted
// by the admin) keeps their claim row, so the lifetime-count check still sees
// them and correctly forces a spin-off rather than an in-place cash-mode
// reassignment. This isolates the gap above to the DeleteCashClaim path
// specifically, and confirms the ordinary "co-recipient redeemed and moved on"
// case the spec calls out is handled correctly.
func TestSecA_CashInPlace_RedeemedCoRecipientStillCounted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	// Funded generously and with a queued mock invoice so the spin-off path's
	// internal funding transfer can actually succeed (mirrors
	// TestHandleCashTransferEvent_TransferIntoCash_MultiSliceWallet_SpinsOffToNewWallet).
	wallet := newFundedCashWallet(t, svc, hub, 200_000)
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-safe-spinoff", Amount: 1000},
	}

	aPrivkey := nostr.GeneratePrivateKey()
	aPubkey, _ := nostr.GetPublicKey(aPrivkey)
	bPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: aPubkey, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: bPubkey, AmountMloki: 1000},
	}))

	// B redeems their slice (simulated by claiming it terminal) — the row
	// stays, ClaimedAt set.
	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, bPubkey)
	require.NoError(t, err)

	claimsAfter, err := svc.AppsService.ListClaimsForWallet(wallet.ID)
	require.NoError(t, err)
	require.Len(t, claimsAfter, 2, "a redeemed co-recipient's row persists and is still counted")

	// A converts to cash mode — must NOT reassign in place, because the wallet's
	// lifetime recipient count is (correctly) still 2.
	_, newSecretHash := cashSecretAndHash(t)
	proof := buildTransferProofEvent(t, aPrivkey, *wallet.WalletPubkey, db.CashIdentityCash, newSecretHash, "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: aPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error)
	result, ok := response.Result.(cashTransferResponse)
	require.True(t, ok, "unexpected result type %T", response.Result)
	assert.NotEmpty(t, result.NewWalletToken,
		"a redeemed co-recipient is still counted, so cash-mode conversion correctly spins off a dedicated wallet")
}
