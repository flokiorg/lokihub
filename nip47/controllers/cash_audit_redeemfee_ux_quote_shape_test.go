package controllers

// UX-audit coverage for the new redeem-fee quote fields on list_recipients
// (NIP-CASH.md §Listing Recipients, §The Redeem Fee), added under
// data/docs/audits/cash-hub-redeem-fee-2026-08-02/. This is a follow-on to the
// prior round's cash_audit_ux_list_recipients_missing_fields_test.go, which
// proved min_transfer_millis and expires_at are absent from the wire response
// — this test proves the OPPOSITE for the redeem fee: the quote a recipient
// needs (redeem_fee_millis / net_redeemable_millis) genuinely IS present on the
// wire, at the one place NIP-CASH says a recipient should look
// ("list_recipients... reports the exact fee and net amount for every slice,
// so a recipient always knows precisely what cash_redeem will pay out before
// they call it" — §The Redeem Fee).
//
// Marshals the real wire response (not just the Go struct) exactly the way a
// recipient's NWC client would receive it, mirroring the prior round's own
// technique.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// TestHandleListRecipientsEvent_RedeemFeeQuote_PresentOnTheWire proves the
// redeem-fee quote fields are genuinely reachable by a recipient, in contrast
// to the prior round's C1/H1 findings about expires_at/min_transfer_millis. It
// also documents the actual field names/shape a client author has to work
// against, since NIP-CASH's own example response (§Listing Recipients) is
// illustrative prose, not something guaranteed to match byte-for-byte.
func TestHandleListRecipientsEvent_RedeemFeeQuote_PresentOnTheWire(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 21_000)

	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	// 1% fee (10_000 ppm) on a 21,000 mloki slice: fee 210, net 20,790 — the
	// exact figures NIP-CASH.md's own §Listing Recipients example response
	// uses, so this test's assertions double as a spec-conformance check.
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 21_000, RedeemFeePpm: 10_000},
	}))

	nip47Request := &models.Request{Method: constants.NIP47MethodListRecipients}
	var response *models.Response
	NewTestNip47Controller(svc).HandleListRecipientsEvent(context.TODO(), nip47Request, 1, wallet, func(r *models.Response, _ nostr.Tags) {
		response = r
	})
	require.Nil(t, response.Error)

	// Marshal exactly what goes over the wire to a recipient's NWC client —
	// not the Go struct — same discipline the prior audit round's
	// cash_audit_ux_list_recipients_missing_fields_test.go used.
	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	rawStr := string(raw)
	t.Logf("actual list_recipients wire response: %s", rawStr)

	assert.Contains(t, rawStr, `"redeem_fee_millis":210`,
		"a recipient's client can read the quoted fee directly off the wire, by this exact field name")
	assert.Contains(t, rawStr, `"net_redeemable_millis":20790`,
		"a recipient's client can read the quoted net payout directly off the wire, by this exact field name")

	// Sanity: the fields sit on the SAME per-recipient row as identity_value/
	// amount_millis — a client doesn't need a second call or a join to build
	// its invoice amount from this response alone.
	assert.Contains(t, rawStr, `"amount_millis":21000`)
	assert.Contains(t, rawStr, pk)
}

// TestHandleListRecipientsEvent_RedeemFeeQuote_ZeroFee_StillExplicit proves
// that even a free (0 ppm) slice still carries an explicit, non-omitted
// redeem_fee_millis:0 / net_redeemable_millis==amount_millis pair, rather than
// the fields disappearing from the JSON entirely for the common "no fee
// configured" case (nipcash.RecipientStatus has no `,omitempty` on either field —
// list_recipients_controller.go's nipcash.RecipientStatus struct). This matters for a
// naive client that treats "field absent" and "field present with value 0"
// differently, or that only tests its fee-parsing code path against a
// nonzero-fee fixture during development.
func TestHandleListRecipientsEvent_RedeemFeeQuote_ZeroFee_StillExplicit(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 5000)

	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 5000}, // RedeemFeePpm defaults to 0
	}))

	nip47Request := &models.Request{Method: constants.NIP47MethodListRecipients}
	var response *models.Response
	NewTestNip47Controller(svc).HandleListRecipientsEvent(context.TODO(), nip47Request, 1, wallet, func(r *models.Response, _ nostr.Tags) {
		response = r
	})
	require.Nil(t, response.Error)

	raw, err := json.Marshal(response.Result)
	require.NoError(t, err)
	rawStr := string(raw)
	t.Logf("actual list_recipients wire response (zero-fee slice): %s", rawStr)

	assert.Contains(t, rawStr, `"redeem_fee_millis":0`)
	assert.Contains(t, rawStr, `"net_redeemable_millis":5000`)
}
