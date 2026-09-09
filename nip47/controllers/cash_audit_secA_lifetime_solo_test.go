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

// TestSecA_BearerInPlace_AfterCoRecipientDeleted_LeaksOntoSharedConnection
// demonstrates independent-audit-A Finding: cash_transfer's "convert a slice to
// bearer in place" eligibility check counts the wallet's *current* claim rows
// (AppsService.ListClaimsForWallet) instead of the wallet's *lifetime*
// recipient set, as NIP-CASH §"Which outcome a request produces" and
// §Security Considerations both require ("evaluated against every recipient the
// wallet has EVER had, not just currently-unclaimed ones").
//
// A claim row is normally never removed for the wallet's lifetime — a redeem or
// full split sets ClaimedAt but keeps the row, so a co-recipient who redeemed
// and moved on is still correctly counted. The one operation that DELETES a row
// is the admin's own DeleteCashClaim ("removing one bad recipient from a shared
// wallet", api.DeleteCashClaim / apps.DeleteCashClaim). After that admin action,
// the wallet's live claim count drops below its true lifetime recipient count,
// and the check wrongly treats a historically-multi-recipient wallet as
// lifetime-solo — allowing an in-place bearer reassignment onto a connection
// the removed co-recipient STILL holds (the connection secret is deterministic
// and was broadcast at creation; deleting a slice never rotates it).
//
// The removed co-recipient can then decrypt the eventual bearer cash_redeem
// request (its raw secret travels in the request body over that shared
// connection) and front-run the redemption — exactly the theft the
// spin-off-to-a-dedicated-wallet rule exists to prevent.
//
// This test proves the insecure PATH is reachable: it asserts the transfer is
// resolved in place (no NewWalletToken minted, bearer slice lands on the SAME
// formerly-shared wallet) rather than spun off into a fresh dedicated wallet.
func TestSecA_BearerInPlace_AfterCoRecipientDeleted_LeaksOntoSharedConnection(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	// A wallet that HAS ALWAYS HAD two recipients (A and B). Both were handed
	// the same shared connection at creation.
	wallet := newFundedCashWallet(t, svc, hub, 2000)

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
	// holds the (unchanged) shared connection secret.
	bClaim := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, bPubkey)
	_, err = svc.AppsService.DeleteCashClaim(wallet.ID, bClaim.ID)
	require.NoError(t, err)

	claimsAfter, err := svc.AppsService.ListClaimsForWallet(wallet.ID)
	require.NoError(t, err)
	require.Len(t, claimsAfter, 1, "the count the eligibility check relies on has dropped below the true lifetime count")

	// A converts their slice to bearer via a FULL transfer. Because the live
	// claim count is now 1, the controller treats this as a lifetime-solo
	// wallet and reassigns IN PLACE instead of spinning off a dedicated wallet.
	_, newSecretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, aPrivkey, *wallet.WalletPubkey, db.CashIdentityBearer, newSecretHash, "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: aPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error)
	result, ok := response.Result.(cashTransferResponse)
	require.True(t, ok, "unexpected result type %T", response.Result)

	// THE VULNERABILITY: the bearer note was reassigned in place onto the SAME
	// wallet whose connection B still holds — not spun off into a fresh,
	// never-shared dedicated wallet. A spec-correct implementation would return
	// a NewWalletToken here (spin-off) and would NOT leave the bearer slice on
	// this shared wallet.
	assert.Empty(t, result.NewWalletToken,
		"SECURITY: bearer conversion resolved IN PLACE on a connection a removed co-recipient still holds")
	bearerSliceOnSharedWallet := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityBearer, newSecretHash)
	require.NotNil(t, bearerSliceOnSharedWallet,
		"SECURITY: the bearer slice now lives on the formerly-shared wallet; the removed co-recipient B can decrypt its future cash_redeem and steal the secret")
	assert.Nil(t, bearerSliceOnSharedWallet.ClaimedAt)
}

// TestSecA_BearerInPlace_RedeemedCoRecipientStillCounted is the paired
// CONFIRMED-SAFE case: a co-recipient who REDEEMED (rather than being deleted
// by the admin) keeps their claim row, so the lifetime-count check still sees
// them and correctly forces a spin-off rather than an in-place bearer
// reassignment. This isolates the gap above to the DeleteCashClaim path
// specifically, and confirms the ordinary "co-recipient redeemed and moved on"
// case the spec calls out is handled correctly.
func TestSecA_BearerInPlace_RedeemedCoRecipientStillCounted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	// Funded generously and with a queued mock invoice so the spin-off path's
	// internal funding transfer can actually succeed (mirrors
	// TestHandleCashTransferEvent_TransferIntoBearer_MultiSliceWallet_SpinsOffToNewWallet).
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

	// A converts to bearer — must NOT reassign in place, because the wallet's
	// lifetime recipient count is (correctly) still 2.
	_, newSecretHash := bearerSecretAndHash(t)
	proof := buildTransferProofEvent(t, aPrivkey, *wallet.WalletPubkey, db.CashIdentityBearer, newSecretHash, "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: aPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: newSecretHash},
	})

	require.Nil(t, response.Error)
	result, ok := response.Result.(cashTransferResponse)
	require.True(t, ok, "unexpected result type %T", response.Result)
	assert.NotEmpty(t, result.NewWalletToken,
		"a redeemed co-recipient is still counted, so bearer conversion correctly spins off a dedicated wallet")
}
