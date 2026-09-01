package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func onePubkeyRecipientJSON(pubkey string, amountMloki uint64) string {
	return fmt.Sprintf(`{"identity_type":"pubkey","identity_value":"%s","amount_millis":%d}`, pubkey, amountMloki)
}

func makeCashWalletRequest(pubkey string, amountMloki uint64, expirationSecs int) string {
	return fmt.Sprintf(`{
		"method": "mint_cash",
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

func TestHandleMintCashEvent_NotCashHub(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A standard (non-cash_hub) app.
	standardApp, _, err := svc.AppsService.CreateApp("std", "", 0, "never", nil, []string{constants.GET_INFO_SCOPE}, db.AppKindStandard, nil, "", nil)
	require.NoError(t, err)

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCashWalletRequest(beneficiaryPubkey, 1000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, standardApp, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, publishedResponse.Error.Code)
}

func TestHandleMintCashEvent_SumOfRecipients_ExceedsPerWalletMaxTotal(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 5000, 3600) // max 5000 mloki total per wallet

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCashWalletRequest(beneficiaryPubkey, 6000, 3600)), nip47Request) // 6000 > 5000
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, publishedResponse.Error.Code)
}

func TestHandleMintCashEvent_ExpiryExceedsMax(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600) // max 3600 secs

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCashWalletRequest(beneficiaryPubkey, 1000, 7200)), nip47Request) // 7200 > 3600
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
}

// An omitted/zero expiry must default to the hub's own max_exp_secs rather
// than producing an already-expired wallet (time.Now() + 0).
func TestHandleMintCashEvent_OmittedExpiry_DefaultsToHubMax(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600) // max 3600 secs
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	// No "expiry" field at all (defaults to zero value on unmarshal).
	err = json.Unmarshal([]byte(fmt.Sprintf(`{
		"method": "mint_cash",
		"params": {
			"recipients": [%s]
		}
	}`, onePubkeyRecipientJSON(beneficiaryPubkey, 1000))), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(mintCashResponse)
	require.NotNil(t, result.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(3600*time.Second), time.Unix(*result.ExpiresAt, 0), 5*time.Second,
		"wallet must default to the hub's max_exp_secs, not expire immediately")
}

func TestHandleMintCashEvent_InsufficientBalance(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	// hub has 0 balance — do NOT call fundApp

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCashWalletRequest(beneficiaryPubkey, 5000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_INSUFFICIENT_BALANCE, publishedResponse.Error.Code)
}

func TestHandleMintCashEvent_RateLimited(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	controller := NewTestNip47Controller(svc)

	// Exhaust the rate limiter.
	for i := 0; i < cashRateLimitPerHour; i++ {
		controller.cashRateLimiter.Allow(hub.AppPubkey, cashRateLimitPerHour)
	}

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCashWalletRequest(beneficiaryPubkey, 1000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	controller.HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, publishedResponse.Error.Code)
}

func TestHandleMintCashEvent_HappyPath_SingleRecipient(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCashWalletRequest(beneficiaryPubkey, 1000, 1800)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(mintCashResponse)
	assert.Contains(t, result.PairingURI, "nostr+walletconnect://")
	assert.NotEmpty(t, result.WalletPubkey)
	require.NotNil(t, result.ExpiresAt)
	assert.Greater(t, *result.ExpiresAt, time.Now().Unix())
	require.Len(t, result.Recipients, 1)
	assert.Equal(t, uint64(1000), result.Recipients[0].AmountMillis)

	// The wire response's cash_token must decode to the exact same
	// wallet pubkey and secret as pairing_uri — the fund-safety property
	// that matters most, since either string alone is a sufficient
	// connection credential (NIP-JW §The Lokicash Token).
	assert.True(t, strings.HasPrefix(result.CashToken, lokicash.HRP+"1"))
	pairingURI, err := url.Parse(result.PairingURI)
	require.NoError(t, err)
	decoded, err := lokicash.Decode(result.CashToken)
	require.NoError(t, err)
	assert.Equal(t, result.WalletPubkey, decoded.WalletPubkey)
	assert.Equal(t, pairingURI.Query().Get("secret"), decoded.Secret)
	assert.Equal(t, pairingURI.Query()["relay"], decoded.RelayURLs)

	// Verify the Cash wallet sub-app was created with correct kind and parent.
	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps)
	require.Equal(t, 1, len(childApps))
	assert.Equal(t, db.ParentKindCash, childApps[0].ParentKind)

	// Hardened scope surface: exactly cash_redeem + cash_transfer + get_balance.
	var perms []db.AppPermission
	svc.DB.Where("app_id = ?", childApps[0].ID).Find(&perms)
	scopes := make([]string, len(perms))
	for i, p := range perms {
		scopes[i] = p.Scope
	}
	assert.ElementsMatch(t, []string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.CASH_CONSOLIDATE_SCOPE, constants.GET_BALANCE_SCOPE}, scopes)
}

func TestHandleMintCashEvent_HappyPath_MultipleRecipients_MixedIdentityTypes(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	connKey := tests.RandomHex32()
	iaPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	registerTrustedIA(t, svc, iaPubkey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(fmt.Sprintf(`{
		"method": "mint_cash",
		"params": {
			"recipients": [
				{"identity_type":"pubkey","identity_value":"%s","amount_millis":1000},
				{"identity_type":"connection_key","identity_value":"%s","ia_pubkey":"%s","amount_millis":2000}
			],
			"expiry": 1800
		}
	}`, pk1, connKey, iaPubkey)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(mintCashResponse)
	require.Len(t, result.Recipients, 2)

	// Exactly one shared wallet app.
	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps)
	require.Len(t, childApps, 1)

	var claims []db.CashWalletClaim
	svc.DB.Where("wallet_app_id = ?", childApps[0].ID).Find(&claims)
	require.Len(t, claims, 2)
}

// TestHandleMintCashEvent_TransferFailure_Rollback verifies that when
// SendPaymentSync fails after the child app is already created, the child app
// (and its claim rows) are deleted (rolled back) and an error is returned.
func TestHandleMintCashEvent_TransferFailure_Rollback(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	// Make the next SendPaymentSync call return an error.
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.PayInvoiceResponses = []*lnclient.PayInvoiceResponse{nil}
	mockLN.PayInvoiceErrors = []error{errors.New("simulated payment failure")}

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCashWalletRequest(beneficiaryPubkey, 1000, 1800)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	// Handler must return an error.
	assert.NotNil(t, publishedResponse.Error, "transfer failure must produce an error response")

	// The child Cash wallet app must have been deleted (rollback).
	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps)
	assert.Empty(t, childApps, "failed Cash wallet creation must roll back the child app")

	var claims []db.CashWalletClaim
	svc.DB.Find(&claims)
	assert.Empty(t, claims, "claim rows must be rolled back too (FK cascade)")
}

func TestHandleMintCashEvent_ConnectionKeyMode_UntrustedIARejected(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	connKey := tests.RandomHex32()
	iaPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	// Deliberately not registered as trusted.

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(fmt.Sprintf(`{
		"method": "mint_cash",
		"params": {
			"recipients": [{"identity_type":"connection_key","identity_value":"%s","ia_pubkey":"%s","amount_millis":1000}],
			"expiry": 1800
		}
	}`, connKey, iaPubkey)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
}

func TestHandleMintCashEvent_NonRoundMloki_FullDrainSucceeds(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	// 1234 mloki isn't a round number of loki, exercising the sat-rounding
	// path in CreateApp's MaxAmountLoki (mloki/1000).
	err = json.Unmarshal([]byte(makeCashWalletRequest(beneficiaryPubkey, 1234, 1800)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.Nil(t, publishedResponse.Error)
}

func TestHandleMintCashEvent_BudgetRenewalNever(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryKey := nostr.GeneratePrivateKey()
	beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCashWalletRequest(beneficiaryPubkey, 1000, 1800)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})
	require.Nil(t, publishedResponse.Error)

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps)
	require.Len(t, childApps, 1)

	var perm db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", childApps[0].ID, constants.CASH_REDEEM_SCOPE).First(&perm).Error)
	assert.Equal(t, constants.BUDGET_RENEWAL_NEVER, perm.BudgetRenewal, "a Cash wallet must never renew, even implicitly")
}

// TestHandleMintCashEvent_Bearer_HappyPath and
// TestHandleMintCashEvent_Bearer_RejectsMixedRecipients close a coverage gap
// flagged by the 2026-08-02 QA audit: the bearer sole-recipient rule (§Bearer
// Slices) was already covered at the cashwallet unit layer, the admin HTTP
// API (api.TestCreateCashWallet_Bearer_*), and the live integration wire, but
// had no fast, offline test at this controller layer — meaning the
// controller's own error-code mapping for the rejection path
// (mapCashWalletErrorCode(ErrInvalidParams) -> ERROR_BAD_REQUEST) was only
// proven by the slow live suite.
func TestHandleMintCashEvent_Bearer_HappyPath(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(`{
		"method": "mint_cash",
		"params": {
			"recipients": [{"identity_type":"bearer","amount_millis":1000}],
			"expiry": 1800
		}
	}`), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(mintCashResponse)
	require.Len(t, result.Recipients, 1)
	assert.Equal(t, db.CashIdentityBearer, result.Recipients[0].IdentityType)
	assert.NotEmpty(t, result.Recipients[0].BearerSecret, "the plaintext secret must be returned exactly once")
	assert.Empty(t, result.Recipients[0].IdentityValue)
}

func TestHandleMintCashEvent_Bearer_RejectsMixedRecipients(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(fmt.Sprintf(`{
		"method": "mint_cash",
		"params": {
			"recipients": [
				{"identity_type":"pubkey","identity_value":"%s","amount_millis":500},
				{"identity_type":"bearer","amount_millis":500}
			],
			"expiry": 1800
		}
	}`, pk)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).HandleMintCashEvent(ctx, nip47Request, dbRequestEvent.ID, hub, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	require.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps)
	assert.Empty(t, childApps, "a rejected request must leave no partial wallet")
}
