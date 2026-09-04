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
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/tests"
)

// TestHandleCashTransferEvent_BearerCurrentPartialSplit_DeliversTokenInClear is
// a regression test for a Low-severity finding from the 2026-07-30 Cash Hub
// audit: a bearer-current caller's partial split has no identity_event to
// draw a delivery pubkey from (recipientPubkey is ""), so attempting the
// normal inner-encryption delivery failed outright — the split had already
// moved real funds into the new wallet by that point, stranding them
// undeliverable. NIP-CASH "Spinning a Slice Off Into a Dedicated Wallet"
// explicitly permits delivering new_wallet_token in the clear for exactly
// this case, since a bearer slice's wallet is structurally single-recipient
// (no co-holder the encryption defends against). Fixed by skipping the inner
// encryption entirely when recipientPubkey == "".
func TestHandleCashTransferEvent_BearerCurrentPartialSplit_DeliversTokenInClear(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, 200_000, tests.RandomHex32())
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	// A partial split funds two internal transfers (carved + remainder).
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-carved", Amount: 2000},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, PaymentHash: tests.MockLNClientHoldTransaction.PaymentHash, Preimage: "preimage-remainder", Amount: 3000},
	}

	secretHex, secretHash := bearerSecretAndHash(t)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityBearer, IdentityValue: secretHash, AmountMloki: 5000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	amount := uint64(2000)
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		BearerSecret: secretHex,
		NewIdentity:  cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
		AmountMillis: &amount,
	})

	require.Nil(t, response.Error, "a bearer-current partial split must succeed and deliver its token, not strand funds")
	result, ok := response.Result.(cashTransferResponse)
	require.True(t, ok, "unexpected result type %T", response.Result)
	require.NotEmpty(t, result.NewWalletToken)

	// Both tokens (carved + remainder) must be PLAIN lokicash1... strings —
	// decodable directly, no NIP-44 decryption involved (there is no co-holder
	// to encrypt against for a bearer-current caller, whose source wallet is
	// structurally single-recipient).
	tok, err := lokicash.Decode(result.NewWalletToken)
	require.NoError(t, err, "bearer-current carved delivery must be a plain, directly-decodable lokicash token")
	assert.Equal(t, result.NewWalletPubkey, tok.WalletPubkey)

	require.NotEmpty(t, result.RemainderWalletToken, "the remainder is now its own new dedicated wallet")
	remTok, err := lokicash.Decode(result.RemainderWalletToken)
	require.NoError(t, err, "bearer-current remainder delivery must also be a plain token")
	assert.Equal(t, result.RemainderWalletPubkey, remTok.WalletPubkey)
	require.NotNil(t, result.RemainingAmountMillis)
	assert.EqualValues(t, 3000, *result.RemainingAmountMillis)

	// The source bearer slice is consumed whole (terminal) — its value re-emerged
	// as the two new bearer/pubkey wallets above, never decremented in place.
	sourceClaim := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityBearer, secretHash)
	require.NotNil(t, sourceClaim)
	require.NotNil(t, sourceClaim.ClaimedAt)
}

// TestHandleCashTransferEvent_InPlaceReassignment_LostRace_ReleasesProofGuard
// is a regression test for a second Low-severity finding from the same
// audit: a full in-place reassignment that loses its optimistic-lock race
// (ReassignCashSliceIdentity returns ErrNotFound because a concurrent
// operation already claimed/transferred the slice) left the just-inserted
// db.CashTransferProof row consumed — even though the losing request itself
// authorized nothing. Unlike the split path, ReassignCashSliceIdentity is a
// single atomic UPDATE with no fund movement of its own, so a failure here
// means nothing happened at all: the proof must stay usable for a retry,
// exactly as handleCashTransferSplit's own rollback already does for the
// split path.
func TestHandleCashTransferEvent_InPlaceReassignment_LostRace_ReleasesProofGuard(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 1000},
	}))

	// Claim the slice out from under the upcoming transfer FIRST (simulating
	// a concurrent operation that already won), so ReassignCashSliceIdentity
	// is guaranteed to lose its race.
	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, currentPubkey)
	require.NoError(t, err)

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 1000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})
	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_NOT_FOUND, response.Error.Code)

	// The replay guard must have been released: a fresh attempt reusing the
	// EXACT SAME proof (not a newly-signed one) must not be rejected as
	// "already used" — it must instead fail for the real, original reason
	// (the slice is claimed, nothing to transfer), proving the guard row was
	// deleted rather than left consumed.
	retry := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})
	require.NotNil(t, retry.Error)
	assert.Equal(t, constants.ERROR_NOT_FOUND, retry.Error.Code, "must fail for the same underlying reason, not \"identity_event has already been used\"")
}
