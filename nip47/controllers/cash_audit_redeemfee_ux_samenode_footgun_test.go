package controllers

// UX-audit coverage for the "worst-case ceiling, never a floor" property
// NIP-CASH.md's §Listing Recipients documents for the redeem fee quote. This
// test plays out the exact scenario a well-behaved, spec-following recipient
// client would follow:
//
//  1. Call list_recipients, read net_redeemable_millis for your own slice —
//     the value NIP-CASH explicitly tells a client to build its invoice from
//     ("so a recipient always knows precisely what cash_redeem will pay out
//     before they call it" — §The Redeem Fee).
//  2. Build a real invoice for exactly that quoted amount.
//  3. Call cash_redeem.
//
// Per NIP-CASH's own §Listing Recipients text, this is explicitly allowed to
// underpay relative to what actually gets paid out ("A slice's eventual
// cash_redeem MAY pay out more than net_redeemable_millis here... it will
// never pay out less") — but the wire behavior is not "pay out more than the
// invoice asked for," it's outright REJECTION: cash_redeem's exact-match rule
// (cash_redeem_controller.go step 9) requires the invoice to match whatever
// this SPECIFIC redemption's actual required amount turns out to be, and for
// a same-node resolution that's the FULL slice amount, not the quoted net.
//
// Originally (2026-08-02 UX audit finding H1) this rejection's message never
// named the mechanism — a recipient could see THAT it failed and even the
// exact math, but not WHY the amount they were quoted stopped applying.
// cash_redeem_controller.go step 9 was fixed to name the same-node mechanism
// explicitly whenever that's the reason for the mismatch; this test now
// confirms the fix rather than merely documenting the gap.

import (
	"context"
	"strings"
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

// TestHandleCashRedeemEvent_QuotedNetAmount_RejectedWhenRedemptionResolvesSameNode
// is the concrete "did the spec's documented ceiling behavior turn into a
// footgun" test the audit asked for. A recipient who did everything the spec
// tells them to do — quote via list_recipients, build the invoice for the
// quoted net_redeemable_millis — still gets a hard rejection when this
// specific redemption happens to resolve same-node; this confirms the
// rejection message now explains why.
func TestHandleCashRedeemEvent_QuotedNetAmount_RejectedWhenRedemptionResolvesSameNode(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Make this specific invoice resolve as same-node, exactly like
	// TestHandleCashRedeemEvent_SameNodeRedemption_FeeFree_FullAmountPaid
	// does — transactions.IsSelfPayment matches an outgoing payment against a
	// pending incoming Transaction row for the same payment hash, paid to the
	// node's own pubkey.
	svc.LNClient.(*tests.MockLn).Pubkey = mockZeroAmountInvoicePayeePubkey
	mockPreimage := "same-node-preimage-footgun"
	require.NoError(t, svc.DB.Create(&db.Transaction{
		State:          constants.TRANSACTION_STATE_PENDING,
		Type:           constants.TRANSACTION_TYPE_INCOMING,
		PaymentRequest: tests.MockZeroAmountInvoice,
		PaymentHash:    tests.MockZeroAmountPaymentHash,
		Preimage:       &mockPreimage,
		AmountMloki:    1000,
	}).Error)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	// 10% redeem fee: list_recipients will quote redeem_fee_millis=100,
	// net_redeemable_millis=900 for this slice.
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: 1000, RedeemFeePpm: 100_000},
	}))

	// Step 1: the recipient calls list_recipients, exactly as NIP-CASH's
	// §The Redeem Fee tells them to, and reads their own net_redeemable_millis.
	controller := NewTestNip47Controller(svc)
	var listResp *models.Response
	controller.HandleListRecipientsEvent(context.TODO(), &models.Request{Method: constants.NIP47MethodListRecipients}, 1, wallet,
		func(r *models.Response, _ nostr.Tags) { listResp = r })
	require.Nil(t, listResp.Error)
	quoted := listResp.Result.(listRecipientsResponse).Recipients[0]
	require.Equal(t, claimantPubkey, quoted.IdentityValue)
	require.Equal(t, int64(900), quoted.NetRedeemableMillis, "sanity: this is the exact figure a spec-following client would build its invoice from")

	// Step 2 + 3: the recipient builds a real invoice for exactly the quoted
	// net_redeemable_millis (900) and calls cash_redeem — normally the right
	// move, but THIS redemption resolves same-node (step above), so the
	// wallet's actual required amount is the FULL 1000, fee-free, not 900.
	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	response := handleClaimFundsFor(t, svc, controller, wallet, cashRedeemParams{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(uint64(quoted.NetRedeemableMillis)), //nolint:gosec // test-controlled positive value — exactly what list_recipients quoted
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})

	require.NotNil(t, response.Error, "a spec-following recipient's invoice, built for the exact quoted net_redeemable_millis, is REJECTED outright rather than paid out for more")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
	t.Logf("actual cash_redeem rejection a recipient sees after following list_recipients' own quote: %q", response.Error.Message)

	// The message must now explain WHY the fee dropped to 0 this time (this
	// redemption resolves same-node) and what to do about it (present the
	// full amount instead) — closing the gap H1 identified.
	msg := strings.ToLower(response.Error.Message)
	assert.Contains(t, msg, "same-node", "the rejection message must name the mechanism responsible for the amount changing")
	assert.Contains(t, msg, "1000", "the rejection message must tell the recipient the correct amount to retry with")

	// The claim survives untouched — the recipient CAN retry (per
	// cash_redeem_controller.go's own rollback-on-mismatch step), but only
	// once they've somehow independently worked out they need to resubmit
	// for the FULL 1000, which nothing in this response tells them.
	claim, err := svc.AppsService.GetCashWalletClaim(wallet.ID, db.CashIdentityPubkey, claimantPubkey)
	require.NoError(t, err)
	require.NotNil(t, claim, "a rejected attempt must not burn the slice, so the recipient gets a chance to retry once they work out the right amount")
}
