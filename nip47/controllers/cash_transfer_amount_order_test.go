package controllers

import (
	"strconv"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Transferring and Splitting a Slice: the caller is verified before the
// amount is bounded by the slice. An unauthenticated request must not learn the
// slice's balance from the error; an authenticated one gets the precise answer.
func TestHandleCashTransferEvent_AmountOverBalance_ProofComesFirst(t *testing.T) {
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

	const balance = 1234
	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, AmountMloki: balance},
	}))
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	tooMuch := uint64(999_999_999)

	// Signed by someone who does not hold the slice.
	strangerPriv := nostr.GeneratePrivateKey()
	stranger := buildTransferProofEvent(t, strangerPriv, *wallet.WalletPubkey, db.CashIdentityPubkey, newPub, "", tooMuch, nil, time.Now())
	refused := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, stranger),
		NewIdentity:  cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
		AmountMillis: &tooMuch,
	})
	require.NotNil(t, refused.Error)
	assert.NotContains(t, refused.Error.Message, strconv.Itoa(balance), "an unauthenticated caller must not learn the balance")

	// The owner's own proof for the same amount: refused for the amount, by name.
	owner := buildTransferProofEvent(t, ownerPriv, *wallet.WalletPubkey, db.CashIdentityPubkey, newPub, "", tooMuch, nil, time.Now())
	bounded := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, owner),
		NewIdentity:  cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
		AmountMillis: &tooMuch,
	})
	require.NotNil(t, bounded.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, bounded.Error.Code)
	assert.Contains(t, bounded.Error.Message, strconv.Itoa(balance), "the slice's holder is told what it holds")
}
