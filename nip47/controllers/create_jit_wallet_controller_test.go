package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func onePubkeyRecipientJSON(pubkey string, amountMloki uint64) string {
	return fmt.Sprintf(`{"identity_type":"pubkey","identity_value":"%s","amount_mloki":%d}`, pubkey, amountMloki)
}

func makeJITWalletRequest(pubkey string, amountMloki uint64, expirationSecs int) string {
	return fmt.Sprintf(`{
		"method": "create_jit_wallet",
		"params": {
			"recipients": [%s],
			"expiry": %d
		}
	}`, onePubkeyRecipientJSON(pubkey, amountMloki), expirationSecs)
}

// registerTrustedIA registers iaPubkey as a trusted Identity Authority on svc's DB.
func registerTrustedIA(t *testing.T, svc *tests.TestService, iaPubkey string) {
	t.Helper()
	_, err := apps.NewIdentityAuthorityManager(svc.DB).Add(iaPubkey, "test-ia", nil)
	require.NoError(t, err)
}

func TestHandleCreateJITWalletEvent_NotJITHub(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A standard (non-jit_hub) app.
	standardApp, _, err := svc.AppsService.CreateApp("std", "", 0, "never", nil, []string{constants.GET_INFO_SCOPE}, db.AppKindStandard, nil, "", nil)
	require.NoError(t, err)

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeJITWalletRequest(beneficiaryPubkey, 1000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, standardApp, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, publishedResponse.Error.Code)
}

func TestHandleCreateJITWalletEvent_SumOfRecipients_ExceedsPerWalletMaxTotal(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 5000, 3600) // max 5000 mloki total per wallet

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeJITWalletRequest(beneficiaryPubkey, 6000, 3600)), nip47Request) // 6000 > 5000
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, publishedResponse.Error.Code)
}

func TestHandleCreateJITWalletEvent_ExpiryExceedsMax(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600) // max 3600 secs

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeJITWalletRequest(beneficiaryPubkey, 1000, 7200)), nip47Request) // 7200 > 3600
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
}

// An omitted/zero expiry must default to the hub's own max_exp_secs rather
// than producing an already-expired wallet (time.Now() + 0).
func TestHandleCreateJITWalletEvent_OmittedExpiry_DefaultsToHubMax(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600) // max 3600 secs
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	// No "expiry" field at all (defaults to zero value on unmarshal).
	err = json.Unmarshal([]byte(fmt.Sprintf(`{
		"method": "create_jit_wallet",
		"params": {
			"recipients": [%s]
		}
	}`, onePubkeyRecipientJSON(beneficiaryPubkey, 1000))), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(createJITWalletResponse)
	assert.WithinDuration(t, time.Now().Add(3600*time.Second), time.Unix(result.ExpiresAt, 0), 5*time.Second,
		"wallet must default to the hub's max_exp_secs, not expire immediately")
}

func TestHandleCreateJITWalletEvent_InsufficientBalance(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	// hub has 0 balance — do NOT call fundApp

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeJITWalletRequest(beneficiaryPubkey, 5000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_INSUFFICIENT_BALANCE, publishedResponse.Error.Code)
}

func TestHandleCreateJITWalletEvent_RateLimited(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	controller := NewTestNip47Controller(svc)

	// Exhaust the rate limiter.
	for i := 0; i < jitRateLimitPerHour; i++ {
		controller.jitRateLimiter.Allow(hub.AppPubkey, jitRateLimitPerHour)
	}

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeJITWalletRequest(beneficiaryPubkey, 1000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	controller.HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, publishedResponse.Error.Code)
}

func TestHandleCreateJITWalletEvent_HappyPath_SingleRecipient(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeJITWalletRequest(beneficiaryPubkey, 1000, 1800)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(createJITWalletResponse)
	assert.Contains(t, result.PairingURI, "nostr+walletconnect://")
	assert.NotEmpty(t, result.WalletPubkey)
	assert.Greater(t, result.ExpiresAt, time.Now().Unix())
	require.Len(t, result.Recipients, 1)
	assert.Equal(t, uint64(1000), result.Recipients[0].AmountMloki)

	// Verify the JIT wallet sub-app was created with correct kind and parent.
	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps)
	require.Equal(t, 1, len(childApps))
	assert.Equal(t, db.ParentKindJIT, childApps[0].ParentKind)

	// Hardened scope surface: exactly jit_claim_funds + get_balance.
	var perms []db.AppPermission
	svc.DB.Where("app_id = ?", childApps[0].ID).Find(&perms)
	scopes := make([]string, len(perms))
	for i, p := range perms {
		scopes[i] = p.Scope
	}
	assert.ElementsMatch(t, []string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE}, scopes)
}

func TestHandleCreateJITWalletEvent_HappyPath_MultipleRecipients_MixedIdentityTypes(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	connKey := tests.RandomHex32()
	iaPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	registerTrustedIA(t, svc, iaPubkey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(fmt.Sprintf(`{
		"method": "create_jit_wallet",
		"params": {
			"recipients": [
				{"identity_type":"pubkey","identity_value":"%s","amount_mloki":1000},
				{"identity_type":"connection_key","identity_value":"%s","ia_pubkey":"%s","amount_mloki":2000}
			],
			"expiry": 1800
		}
	}`, pk1, connKey, iaPubkey)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(createJITWalletResponse)
	require.Len(t, result.Recipients, 2)

	// Exactly one shared wallet app.
	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps)
	require.Len(t, childApps, 1)

	var claims []db.JITWalletClaim
	svc.DB.Where("wallet_app_id = ?", childApps[0].ID).Find(&claims)
	require.Len(t, claims, 2)
}

// TestHandleCreateJITWalletEvent_TransferFailure_Rollback verifies that when
// SendPaymentSync fails after the child app is already created, the child app
// (and its claim rows) are deleted (rolled back) and an error is returned.
func TestHandleCreateJITWalletEvent_TransferFailure_Rollback(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	// Make the next SendPaymentSync call return an error.
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.PayInvoiceResponses = []*lnclient.PayInvoiceResponse{nil}
	mockLN.PayInvoiceErrors = []error{errors.New("simulated payment failure")}

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeJITWalletRequest(beneficiaryPubkey, 1000, 1800)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	// Handler must return an error.
	assert.NotNil(t, publishedResponse.Error, "transfer failure must produce an error response")

	// The child JIT wallet app must have been deleted (rollback).
	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps)
	assert.Empty(t, childApps, "failed JIT wallet creation must roll back the child app")

	var claims []db.JITWalletClaim
	svc.DB.Find(&claims)
	assert.Empty(t, claims, "claim rows must be rolled back too (FK cascade)")
}

func TestHandleCreateJITWalletEvent_ConnectionKeyMode_UntrustedIARejected(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	connKey := tests.RandomHex32()
	iaPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	// Deliberately not registered as trusted.

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(fmt.Sprintf(`{
		"method": "create_jit_wallet",
		"params": {
			"recipients": [{"identity_type":"connection_key","identity_value":"%s","ia_pubkey":"%s","amount_mloki":1000}],
			"expiry": 1800
		}
	}`, connKey, iaPubkey)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
}

func TestHandleCreateJITWalletEvent_NonRoundMloki_FullDrainSucceeds(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	// 1234 mloki isn't a round number of loki, exercising the sat-rounding
	// path in CreateApp's MaxAmountLoki (mloki/1000).
	err = json.Unmarshal([]byte(makeJITWalletRequest(beneficiaryPubkey, 1234, 1800)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.Nil(t, publishedResponse.Error)
}

func TestHandleCreateJITWalletEvent_BudgetRenewalNever(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeJITWalletRequest(beneficiaryPubkey, 1000, 1800)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleCreateJITWalletEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})
	require.Nil(t, publishedResponse.Error)

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps)
	require.Len(t, childApps, 1)

	var perm db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", childApps[0].ID, constants.JIT_CLAIM_FUNDS_SCOPE).First(&perm).Error)
	assert.Equal(t, constants.BUDGET_RENEWAL_NEVER, perm.BudgetRenewal, "a JIT wallet must never renew, even implicitly")
}
