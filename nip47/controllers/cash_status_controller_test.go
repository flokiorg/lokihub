package controllers

import (
	"encoding/json"
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// cash_status is served over the PRIVATE transport only, so every request reaches the
// controller with a caller identity (the item proof's signer) and a scope. These
// fixtures ask for the full roster, which is what they assert on; with scope=all the
// caller is not used for filtering, so any identity stands in.
var (
	fullRosterParams = json.RawMessage(`{"scope":"all"}`)
	fullRosterCaller = &CashStatusCaller{IdentityValue: "test-caller"}
)

// TestHandleCashStatusEvent_HappyPath_ShowsAllRecipientsRegardlessOfCaller
// confirms the deliberately shared/transparent model on the STANDARD transport
// (nil caller): any holder of the connection sees the FULL roster, not just their
// own row — matching the model already accepted for get_balance, and unavoidable
// there since every recipient holds the same connection string.
//
// The private transport scopes to the caller's own row by default instead, which
// it can because each item carries a proof identifying who is asking. See
// cash_status_scope_test.go for that half.
func TestHandleCashStatusEvent_HappyPath_ShowsAllRecipientsRegardlessOfCaller(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 3000)

	pkClaimed, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pkUnclaimed, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pkClaimed, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pkUnclaimed, AmountMloki: 2000},
	}))
	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, pkClaimed)
	require.NoError(t, err)

	nip47Request := &models.Request{Method: constants.NIP47MethodCashStatus, Params: fullRosterParams}
	var response *models.Response
	NewTestNip47Controller(svc).HandleCashStatusEvent(context.TODO(), nip47Request, 1, wallet, func(r *models.Response, _ nostr.Tags) {
		response = r
	}, fullRosterCaller)

	require.Nil(t, response.Error)
	result := response.Result.(nipcash.CashStatusResult)
	require.Len(t, result.Recipients, 2)

	byIdentity := map[string]nipcash.RecipientStatus{}
	for _, r := range result.Recipients {
		byIdentity[r.IdentityValue] = r
	}
	assert.True(t, byIdentity[pkClaimed].Claimed)
	assert.NotNil(t, byIdentity[pkClaimed].ClaimedAt)
	assert.Equal(t, uint64(1000), byIdentity[pkClaimed].AmountMillis)
	assert.False(t, byIdentity[pkUnclaimed].Claimed)
	assert.Nil(t, byIdentity[pkUnclaimed].ClaimedAt)
	assert.Equal(t, uint64(2000), byIdentity[pkUnclaimed].AmountMillis)
}

func TestHandleCashStatusEvent_NonCashWalletApp_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)

	nip47Request := &models.Request{Method: constants.NIP47MethodCashStatus, Params: fullRosterParams}
	var response *models.Response
	NewTestNip47Controller(svc).HandleCashStatusEvent(context.TODO(), nip47Request, 1, hub, func(r *models.Response, _ nostr.Tags) {
		response = r
	}, fullRosterCaller)

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)
}

func TestHandleCashStatusEvent_EmptyWallet_ReturnsEmptyList(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	nip47Request := &models.Request{Method: constants.NIP47MethodCashStatus, Params: fullRosterParams}
	var response *models.Response
	NewTestNip47Controller(svc).HandleCashStatusEvent(context.TODO(), nip47Request, 1, wallet, func(r *models.Response, _ nostr.Tags) {
		response = r
	}, fullRosterCaller)

	require.Nil(t, response.Error)
	result := response.Result.(nipcash.CashStatusResult)
	assert.Empty(t, result.Recipients)
}
