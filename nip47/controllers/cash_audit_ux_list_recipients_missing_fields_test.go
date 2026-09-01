package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// TestHandleListRecipientsEvent_SurfacesMinTransferMlokiAndExpiresAt is the
// UX-audit counterpart to the already-fixed max_transfers finding. FIXED
// 2026-08-31: `list_recipients` — the ONLY NIP-47 method a recipient can call
// to introspect their own slice's state over the shared cash_wallet
// connection — now surfaces both pieces of state NIP-CASH says a recipient
// needs to act correctly, which it used to omit entirely:
//
//  1. min_transfer_millis — the floor below which a cash_transfer split (or
//     the remainder it would leave behind) is rejected. A recipient used to
//     have no way to learn this value before attempting a split; they only
//     learned it from the BAD_REQUEST error text after a failed attempt (see
//     apps/cash_hub_service.go's SplitCashSliceAmount error strings), which
//     also burns a share of the rate-limited cash_transfer/cash_redeem quota
//     (see cash_redeem_controller.go's cashRedeemRateLimitPerHour, shared
//     across BOTH methods and keyed by the connection's own shared
//     app_pubkey — so several failed "let me find the floor" attempts by one
//     recipient could lock out every other co-recipient sharing this wallet).
//  2. the wallet's own expiry (ExpiresAt) — a recipient used to have no
//     protocol-level way to learn when their lokicash stops being
//     redeemable. Neither list_recipients nor the lokicash1... token itself
//     (lokicash/lokicash.go TLV types 0-3: wallet pubkey, relay, secret,
//     identity_required — no expiry field) carries it. NIP-CASH's own
//     §Lifecycle and Deletion promises an expiry-driven sweep, but nowhere
//     did the spec, or this implementation, give the recipient themselves a
//     way to observe the deadline they're racing against.
//
// Both fields were already tracked server-side (db.CashWalletClaim.
// MinTransferMloki; db.App.ExpiresAt) and were already exposed to the Hub
// OWNER via the admin HTTP API (api.CashWalletClaimResponse carries both) and
// rendered in the owner-facing frontend (CashHubAllocations.tsx's deadline
// column, RevealConnectionDialog's "Expires" row) — this test only concerns
// the actual RECIPIENT-facing protocol surface, list_recipients, which was
// the one place this state was still invisible.
func TestHandleListRecipientsEvent_SurfacesMinTransferMlokiAndExpiresAt(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 3000)

	// Give the wallet a concrete, checkable expiry and give the claim a
	// concrete, checkable min_transfer_millis floor, so this test can assert
	// on their actual VALUES being present and correct, not just coincidentally
	// present.
	expiresAt := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Update("expires_at", expiresAt).Error)
	require.NoError(t, svc.DB.First(wallet, wallet.ID).Error) // refresh the in-memory struct the controller reads ExpiresAt from

	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 3000, MinTransferMloki: 500},
	}))

	nip47Request := &models.Request{Method: constants.NIP47MethodListRecipients}
	var response *models.Response
	NewTestNip47Controller(svc).HandleListRecipientsEvent(context.TODO(), nip47Request, 1, wallet, func(r *models.Response, _ nostr.Tags) {
		response = r
	})
	require.Nil(t, response.Error)

	result, ok := response.Result.(listRecipientsResponse)
	require.True(t, ok)
	require.Len(t, result.Recipients, 1)
	recipient := result.Recipients[0]

	assert.Equal(t, int64(500), recipient.MinTransferMillis,
		"a recipient must be able to learn their split floor before attempting a cash_transfer, not only from a failed attempt's error text")
	require.NotNil(t, recipient.ExpiresAt, "a recipient must be able to learn their wallet's redemption deadline via protocol")
	assert.Equal(t, expiresAt.Unix(), *recipient.ExpiresAt)

	// Wire-level confirmation too: the raw JSON a real recipient's NWC client
	// receives must actually carry both fields, not just the Go struct.
	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	rawStr := string(raw)
	assert.Contains(t, rawStr, `"min_transfer_millis":500`)
	assert.Contains(t, rawStr, `"expires_at":`)

	t.Logf("actual list_recipients wire response: %s", rawStr)
}

// TestHandleListRecipientsEvent_ExpiresAtOmittedForNeverExpiringWallet proves
// the "never expires" ceiling (db.App.ExpiresAt == nil) round-trips as an
// OMITTED field, not a zero/null timestamp that could be misread as "already
// expired" — the same nil-safe convention every other cash_wallet-adjacent
// response already uses (mint_cash_controller.go's own ExpiresAt field).
func TestHandleListRecipientsEvent_ExpiresAtOmittedForNeverExpiringWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 0) // MaxExpSecs 0 = "never"
	wallet := newFundedCashWallet(t, svc, hub, 3000)
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Update("expires_at", nil).Error)

	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 3000},
	}))

	nip47Request := &models.Request{Method: constants.NIP47MethodListRecipients}
	var response *models.Response
	NewTestNip47Controller(svc).HandleListRecipientsEvent(context.TODO(), nip47Request, 1, wallet, func(r *models.Response, _ nostr.Tags) {
		response = r
	})
	require.Nil(t, response.Error)

	result, ok := response.Result.(listRecipientsResponse)
	require.True(t, ok)
	require.Len(t, result.Recipients, 1)
	assert.Nil(t, result.Recipients[0].ExpiresAt)

	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "expires_at", "a never-expiring wallet must omit expires_at, not send a null/zero timestamp")
}
