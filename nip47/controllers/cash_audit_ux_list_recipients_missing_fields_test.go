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

// TestHandleListRecipientsEvent_OmitsMinTransferMlokiAndExpiresAt is the
// UX-audit counterpart to the already-fixed max_transfers finding: it proves
// that `list_recipients` — the ONLY NIP-47 method a recipient can call to
// introspect their own slice's state over the shared cash_wallet connection
// — never surfaces either of the two pieces of state NIP-CASH says a
// recipient needs to act correctly:
//
//  1. min_transfer_mloki — the floor below which a cash_transfer split (or
//     the remainder it would leave behind) is rejected. A recipient has no
//     way to learn this value before attempting a split; they only learn it
//     from the BAD_REQUEST error text after a failed attempt (see
//     apps/cash_hub_service.go's SplitCashSliceAmount error strings), which
//     also burns a share of the rate-limited cash_transfer/cash_redeem quota
//     (see cash_redeem_controller.go's cashRedeemRateLimitPerHour, shared
//     across BOTH methods and keyed by the connection's own shared
//     app_pubkey — so several failed "let me find the floor" attempts by one
//     recipient can lock out every other co-recipient sharing this wallet).
//  2. the wallet's own expiry (ExpiresAt) — a recipient has no protocol-level
//     way to learn when their lokicash stops being redeemable. Neither
//     list_recipients nor the lokicash1... token itself (lokicash/lokicash.go
//     TLV types 0-3: wallet pubkey, relay, secret, identity_required — no
//     expiry field) carries it. NIP-CASH's own §Lifecycle and Deletion
//     promises an expiry-driven sweep, but nowhere does the spec, or this
//     implementation, give the recipient themselves a way to observe the
//     deadline they're racing against.
//
// Both fields ARE already tracked server-side (db.CashWalletClaim.
// MinTransferMloki; db.App.ExpiresAt) and ARE already exposed to the Hub
// OWNER via the admin HTTP API (api.CashWalletClaimResponse carries both) and
// rendered in the owner-facing frontend (CashHubAllocations.tsx's deadline
// column, RevealConnectionDialog's "Expires" row) — this test only concerns
// the actual RECIPIENT-facing protocol surface, which is list_recipients.
func TestHandleListRecipientsEvent_OmitsMinTransferMlokiAndExpiresAt(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 3000)

	// Give the wallet a concrete, checkable expiry and give the claim a
	// concrete, checkable min_transfer_mloki floor, so this test can assert
	// on their actual VALUES being absent, not just coincidentally zero.
	expiresAt := time.Now().Add(2 * time.Hour)
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Update("expires_at", expiresAt).Error)

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

	// Marshal exactly what a real recipient's NWC client would receive over
	// the wire, and assert the raw JSON never mentions either field — this is
	// deliberately a wire-level check (not a Go struct-field check) so it
	// fails the moment recipientStatus/listRecipientsResponse's JSON shape
	// changes in either direction.
	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	rawStr := string(raw)

	assert.NotContains(t, rawStr, "min_transfer_mloki",
		"list_recipients response leaks no min_transfer_mloki — a recipient cannot learn their split floor before a failed cash_transfer attempt")
	assert.NotContains(t, rawStr, "expires_at",
		"list_recipients response leaks no expires_at/expiry — a recipient cannot learn their wallet's redemption deadline via protocol at all")
	assert.NotContains(t, rawStr, "500", "the configured min_transfer_mloki value (500) must not appear anywhere in the response")

	t.Logf("actual list_recipients wire response: %s", rawStr)
}
