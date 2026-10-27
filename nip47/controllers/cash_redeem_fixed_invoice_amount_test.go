package controllers

import (
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

// NIP-CASH §Redeeming a Slice: amount MUST NOT alter an invoice that already
// carries one (CASH-redeeming-a-slice-04). tests.MockInvoice is fixed at
// 123,000 mloki.
const fixedInvoiceMloki = 123_000

// redeemFixedInvoice redeems a slice of exactly the invoice's amount against
// tests.MockInvoice, sending amount alongside it, and returns the slice's
// owner and the response.
func redeemFixedInvoice(t *testing.T, svc *tests.TestService, amount uint64) (*db.App, string, *models.Response) {
	t.Helper()
	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, fixedInvoiceMloki)
	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: fixedInvoiceMloki},
	}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockPaymentHash, nil, time.Now())
	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, nipcash.CashRedeemRequest{
		Invoice:       tests.MockInvoice,
		Amount:        ptrUint64(amount),
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})
	require.NotNil(t, response)
	return wallet, claimantPubkey, response
}

func TestHandleCashRedeemEvent_FixedInvoice_DifferentAmountRefused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	wallet, claimant, response := redeemFixedInvoice(t, svc, 2*fixedInvoiceMloki)

	require.NotNil(t, response.Error, "an amount that would alter a fixed-amount invoice must be refused")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
	assert.Empty(t, svc.LNClient.(*tests.MockLn).SendPaymentSyncCalls(), "nothing is paid")

	slice := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, claimant)
	assert.Nil(t, slice.ClaimedAt, "the slice stays redeemable")
}

func TestHandleCashRedeemEvent_FixedInvoice_MatchingAmountStillRefused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// NIP-CASH §Redeeming a Slice step 3: an amount on an invoice that carries one
	// is BAD_REQUEST, even when it matches.
	_, _, response := redeemFixedInvoice(t, svc, fixedInvoiceMloki)

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
	assert.Empty(t, svc.LNClient.(*tests.MockLn).SendPaymentSyncCalls(), "nothing is paid")
}
