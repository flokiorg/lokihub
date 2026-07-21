package controllers

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// TestHandleListRecipientsEvent_HappyPath_ShowsAllRecipientsRegardlessOfCaller
// confirms the deliberately shared/transparent model: any holder of the
// connection sees the FULL roster, not just their own row — matching the
// model already accepted for get_balance.
func TestHandleListRecipientsEvent_HappyPath_ShowsAllRecipientsRegardlessOfCaller(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 3000)

	pkClaimed, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pkUnclaimed, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateJITWalletClaims(wallet.ID, []db.JITWalletClaim{
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pkClaimed, AmountMloki: 1000},
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pkUnclaimed, AmountMloki: 2000},
	}))
	_, err = svc.AppsService.ClaimJITWalletSlice(wallet.ID, db.JITAllocIdentityPubkey, pkClaimed)
	require.NoError(t, err)

	nip47Request := &models.Request{Method: constants.NIP47MethodListRecipients}
	var response *models.Response
	NewTestNip47Controller(svc).HandleListRecipientsEvent(context.TODO(), nip47Request, 1, wallet, func(r *models.Response, _ nostr.Tags) {
		response = r
	})

	require.Nil(t, response.Error)
	result := response.Result.(listRecipientsResponse)
	require.Len(t, result.Recipients, 2)

	byIdentity := map[string]recipientStatus{}
	for _, r := range result.Recipients {
		byIdentity[r.IdentityValue] = r
	}
	assert.True(t, byIdentity[pkClaimed].Claimed)
	assert.NotNil(t, byIdentity[pkClaimed].ClaimedAt)
	assert.Equal(t, int64(1000), byIdentity[pkClaimed].AmountMloki)
	assert.False(t, byIdentity[pkUnclaimed].Claimed)
	assert.Nil(t, byIdentity[pkUnclaimed].ClaimedAt)
	assert.Equal(t, int64(2000), byIdentity[pkUnclaimed].AmountMloki)
}

func TestHandleListRecipientsEvent_NonJITWalletApp_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)

	nip47Request := &models.Request{Method: constants.NIP47MethodListRecipients}
	var response *models.Response
	NewTestNip47Controller(svc).HandleListRecipientsEvent(context.TODO(), nip47Request, 1, hub, func(r *models.Response, _ nostr.Tags) {
		response = r
	})

	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, response.Error.Code)
}

func TestHandleListRecipientsEvent_EmptyWallet_ReturnsEmptyList(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	wallet := newFundedJITWallet(t, svc, hub, 1000)

	nip47Request := &models.Request{Method: constants.NIP47MethodListRecipients}
	var response *models.Response
	NewTestNip47Controller(svc).HandleListRecipientsEvent(context.TODO(), nip47Request, 1, wallet, func(r *models.Response, _ nostr.Tags) {
		response = r
	})

	require.Nil(t, response.Error)
	result := response.Result.(listRecipientsResponse)
	assert.Empty(t, result.Recipients)
}
