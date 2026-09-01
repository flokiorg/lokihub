package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// Security Auditor B (independent round, 2026-08-31). FIXED same round
// (2026-08-31): create_circle_wallet_controller.go now rejects any
// max_amount below 1000 mloki (one whole loki) outright, before the
// truncating "/1000" conversion ever runs, so AppPermission.MaxAmountLoki can
// no longer land on the same sentinel value (0) this codebase uses elsewhere
// to mean "no cap at all". NIP-CW requires max_amount on every request and
// establishes no "0 means unlimited" wire convention for this method, so a
// sub-loki value can only ever have been an accidental truncation, never a
// deliberate "no cap" request — rejecting it outright, rather than rounding
// it to something, is the correct fix.
//
// This file originally proved the bug (a sub-loki max_amount silently
// disabled a Circle Wallet's spend cap entirely — 246x overspend
// demonstrated live). It now proves the fix: every sub-loki request (0 and
// [1, 999] mloki) is rejected with BAD_REQUEST before any wallet is created,
// and the boundary at exactly 1000 mloki still succeeds and is still fully
// enforced (the pre-existing control test below, unchanged).
func TestHandleCreateCircleWalletEvent_SubLokiMaxAmount_Rejected(t *testing.T) {
	ctx := context.TODO()

	for _, maxAmount := range []uint64{0, 1, 500, 999} {
		t.Run(fmt.Sprintf("max_amount_%d", maxAmount), func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			// High per-wallet ceiling and high balance so neither binds —
			// only the requested max_amount itself is under test.
			provider := createCircleHubWithCaps(t, svc, 3600, 10_000_000, 1_000_000_000, "")

			requesterKey := nostr.GeneratePrivateKey()

			nip47Request := &models.Request{}
			require.NoError(t, json.Unmarshal(
				[]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, maxAmount, 3600)), nip47Request))

			dbRequestEvent := &db.RequestEvent{}
			svc.DB.Create(&dbRequestEvent)

			var publishedResponse *models.Response
			NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
				HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
					publishedResponse = r
				})

			require.NotNil(t, publishedResponse)
			require.NotNil(t, publishedResponse.Error,
				"a max_amount below 1000 mloki must be rejected outright, never silently truncated to an unlimited cap")
			assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)

			// No wallet, and therefore no spend-cap-bypassing app, was created.
			var count int64
			require.NoError(t, svc.DB.Model(&db.App{}).
				Where("parent_app_id = ? AND parent_kind = ?", provider.ID, db.ParentKindCircle).
				Count(&count).Error)
			assert.Equal(t, int64(0), count, "a rejected request must not leave a partially-created wallet behind")
		})
	}
}

// End-to-end confirmation that the fix actually closes the exploit path, not
// just the input-validation surface: even if a caller could somehow get past
// the new pre-check (they can't — this is defense-in-depth verification, not
// a bypass finding), a wallet is never left able to spend far beyond its
// requested budget the way the original bug allowed.
func TestHandleCreateCircleWalletEvent_WholeLokiMaxAmount_NoOverspendPossible(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHubWithCaps(t, svc, 3600, 10_000_000, 1_000_000_000, "")
	requesterKey := nostr.GeneratePrivateKey()

	// The smallest amount the fix still accepts: exactly 1 loki.
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal(
		[]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 1000, 3600)), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	require.NotNil(t, publishedResponse)
	require.Nil(t, publishedResponse.Error)
	resp, ok := publishedResponse.Result.(createCircleWalletResponse)
	require.True(t, ok)

	var walletApp db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", resp.WalletPubkey).First(&walletApp).Error)

	var perm db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", walletApp.ID, constants.PAY_INVOICE_SCOPE).First(&perm).Error)
	require.Equal(t, 1, perm.MaxAmountLoki, "1000 mloki must floor to exactly 1 whole loki, not 0")

	// The hub's aggregate-commitment accounting now correctly sees this
	// wallet's real 1000 mloki commitment (not silently undercounted to 0).
	var commitment int64
	require.NoError(t, svc.DB.Table("apps").
		Select("COALESCE(SUM(ap.max_amount_loki * 1000), 0)").
		Joins("JOIN app_permissions ap ON ap.app_id = apps.id AND ap.scope = ?", constants.PAY_INVOICE_SCOPE).
		Where("apps.parent_app_id = ? AND apps.parent_kind = ?", provider.ID, db.ParentKindCircle).
		Scan(&commitment).Error)
	assert.Equal(t, int64(1000), commitment)

	tests.FundApp(svc, walletApp.ID, 200_000, tests.RandomHex32())

	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	// tests.MockInvoice resolves to a fixed 123,000 mloki payment -- far
	// beyond the 1000 mloki cap, even though the wallet holds 200,000 mloki.
	_, err = transactionsSvc.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &walletApp.ID, nil)
	require.Error(t, err, "a 1-loki-capped circle_wallet must not be able to pay out 123,000 mloki, even once funded well beyond its cap")
}

// Control case: the exact same scenario, but max_amount is a whole loki
// (1000 mloki) so it does NOT truncate to zero. The same oversized payment
// must now be rejected on budget grounds -- proving the bug above is
// specifically the sub-loki truncation, not a general failure of the
// per-wallet budget cap mechanism.
func TestHandleCreateCircleWalletEvent_WholeLokiMaxAmount_BudgetCapEnforced(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHubWithCaps(t, svc, 3600, 10_000_000, 1_000_000_000, "")

	requesterKey := nostr.GeneratePrivateKey()

	// 1000 mloki == exactly 1 loki: the smallest amount that does NOT floor to zero.
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal(
		[]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 1000, 3600)), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	require.NotNil(t, publishedResponse)
	require.Nil(t, publishedResponse.Error)
	resp, ok := publishedResponse.Result.(createCircleWalletResponse)
	require.True(t, ok)

	var walletApp db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", resp.WalletPubkey).First(&walletApp).Error)

	var perm db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", walletApp.ID, constants.PAY_INVOICE_SCOPE).First(&perm).Error)
	require.Equal(t, 1, perm.MaxAmountLoki, "1000 mloki must floor to exactly 1 whole loki, not 0")

	tests.FundApp(svc, walletApp.ID, 200_000, tests.RandomHex32())

	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err = transactionsSvc.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &walletApp.ID, nil)
	require.Error(t, err, "a 1-loki-capped circle_wallet must not be able to pay out 123,000 mloki")
}
