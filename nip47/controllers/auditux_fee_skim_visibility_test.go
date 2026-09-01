package controllers

// Experience/UX Reviewer — independent finding (2026-08-31 circle/cash
// round). FIXED same round: pay_invoice/pay_keysend's response and
// list_transactions' per-transaction row now carry an optional
// fee_skim_mloki field (see payResponse.FeeSkimMloki / nip47/models.
// Transaction.FeeSkimMloki), populated from the already-correct
// db.Transaction.FeeSkimMloki whenever the paying app is a circle_wallet.
//
// Before this fix, a Circle Wallet member had no NWC-facing way to learn why
// their balance dropped by more than invoice_amount+fees_paid: the
// circle-hub forwarding-fee skim (CircleHubConfig.FeesPpm) was always
// correctly computed and debited (transactions/circle_fee_skim_test.go
// covers that arithmetic in depth), but only ever exposed via the admin HTTP
// API (api/transactions.go's FeeSkim field), which only the wallet
// owner/operator's JWT-gated connection can reach — never the member's own
// NWC connection. This file only ADDS regression tests proving the field now
// reaches the wire; it does not change the fee-skim arithmetic itself.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// newAuditUXCircleWalletApp mirrors transactions/circle_fee_skim_test.go's
// own newCircleHub/newCircleWallet helpers (unexported to that package, so
// duplicated here at the same fixed numbers) — a circle_hub at feesPpm and
// one circle_wallet child, both funded/scoped for a real pay_invoice/
// pay_keysend call through the full NWC controller layer.
func newAuditUXCircleWalletApp(t *testing.T, svc *tests.TestService, feesPpm int, balanceMloki uint64) *db.App {
	t.Helper()
	hub, _, err := svc.AppsService.CreateCircleHub(
		"circle-hub", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "circle-hub-identity", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{
			MaxExpSecs:        3600,
			FeesPpm:           feesPpm,
			PerWalletMaxMloki: 10_000_000,
			MinBudgetRenewal:  constants.BUDGET_RENEWAL_NEVER,
		},
	)
	require.NoError(t, err)
	wallet, _, err := svc.AppsService.CreateApp(
		"circle-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &hub.ID, db.ParentKindCircle, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, balanceMloki, tests.RandomHex32())
	return wallet
}

// TestHandlePayInvoiceEvent_CircleWallet_SurfacesFeeSkim proves the fix over
// pay_invoice: the same 1%-fee/123,000-mloki-payment/1,230-mloki-skim
// scenario transactions/circle_fee_skim_test.go's
// TestSendPaymentSync_CircleWallet_FeeSkim_HappyPath proves at the
// transactions-service layer, now also checked at the NWC wire-response
// layer.
func TestHandlePayInvoiceEvent_CircleWallet_SurfacesFeeSkim(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	wallet := newAuditUXCircleWalletApp(t, svc, 10_000, 140_000) // 1% (10,000 ppm)

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(nip47PayInvoiceJson), nip47Request))
	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).
		HandlePayInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, wallet, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		}, nostr.Tags{})

	require.Nil(t, publishedResponse.Error)
	result, ok := publishedResponse.Result.(payResponse)
	require.True(t, ok)
	assert.Equal(t, uint64(1_230), result.FeeSkimMloki, "a circle_wallet's pay_invoice response must surface the fee skim it was actually charged")

	raw, err := json.Marshal(publishedResponse.Result)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"fee_skim_mloki":1230`)
}

// TestHandlePayKeysendEvent_CircleWallet_SurfacesFeeSkim is the pay_keysend
// counterpart — a separate code path (payKeysend, not pay) that could easily
// have been fixed in only one of the two without a test catching the gap.
func TestHandlePayKeysendEvent_CircleWallet_SurfacesFeeSkim(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	wallet := newAuditUXCircleWalletApp(t, svc, 10_000, 140_000)

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(nip47KeysendJson), nip47Request))
	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).
		HandlePayKeysendEvent(ctx, nip47Request, dbRequestEvent.ID, wallet, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		}, nostr.Tags{})

	require.Nil(t, publishedResponse.Error)
	result, ok := publishedResponse.Result.(payResponse)
	require.True(t, ok)
	assert.Equal(t, uint64(1_230), result.FeeSkimMloki, "a circle_wallet's pay_keysend response must surface the fee skim it was actually charged")
}

// TestHandlePayInvoiceEvent_NonCircleWallet_NoFeeSkimField is the control:
// a plain isolated app (no circle_hub parent, no FeesPpm) must never carry
// this field on the wire at all (omitempty, zero value) — the field's
// presence is specific to circle_wallet payments, not a generic addition to
// every payment response.
func TestHandlePayInvoiceEvent_NonCircleWallet_NoFeeSkimField(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Create(&db.AppPermission{AppId: app.ID, App: *app, Scope: constants.PAY_INVOICE_SCOPE}).Error)

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(nip47PayInvoiceJson), nip47Request))
	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

	var publishedResponse *models.Response
	NewTestNip47Controller(svc).
		HandlePayInvoiceEvent(ctx, nip47Request, dbRequestEvent.ID, app, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		}, nostr.Tags{})

	require.Nil(t, publishedResponse.Error)
	result, ok := publishedResponse.Result.(payResponse)
	require.True(t, ok)
	assert.Zero(t, result.FeeSkimMloki)

	raw, err := json.Marshal(publishedResponse.Result)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "fee_skim_mloki", "a non-circle_wallet payment must omit fee_skim_mloki entirely, not send a zero value")
}

// TestToNip47Transaction_SurfacesFeeSkim covers list_transactions' own
// mapping (nip47/models.ToNip47Transaction), the third of the three
// NWC-facing surfaces this finding named — pay_invoice/pay_keysend's own
// response only tells a member about the payment they just made;
// list_transactions is what lets them reconcile their balance against their
// own history after the fact.
func TestToNip47Transaction_SurfacesFeeSkim(t *testing.T) {
	dbTx := &db.Transaction{
		Type:         constants.TRANSACTION_TYPE_OUTGOING,
		State:        constants.TRANSACTION_STATE_SETTLED,
		AmountMloki:  123_000,
		FeeSkimMloki: 1_230,
		CreatedAt:    time.Now(),
	}
	nip47Tx := models.ToNip47Transaction(dbTx)
	assert.Equal(t, uint64(1_230), nip47Tx.FeeSkimMloki)
}
