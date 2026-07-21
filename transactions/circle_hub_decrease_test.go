package transactions

// A circle_hub is deliberately never granted pay_invoice scope over NWC
// (see nip47/controllers/create_circle_wallet_controller.go) — its own NWC
// connection can only ever create circle wallets, since that connection may
// be shared/public. But an admin should still be able to manually decrease
// a circle_hub's own balance via the admin-only Transfer API
// (api/transactions.go), which already tags this as an internal transfer.
//
// validateCanPay now uses a LEFT JOIN against app_permissions so a payer
// with no PayCapableScopes row (like a circle_hub) still resolves its
// kind/balance, and skips the "app does not have pay_invoice scope" error
// specifically when skipBudgetCap (internal_transfer) is set — never
// reachable from a real NWC pay_invoice/keysend/claim_funds request, which
// all strip that flag from caller-supplied metadata before reaching here.
//
// These three tests cover: the fix itself (internal transfer succeeds
// despite no scope), the regression guard (a non-internal payment attempt
// from the same scope-less app must still be rejected — this must never
// become reachable from a real NWC caller), and that the real-balance check
// still fully applies to an internal transfer (skipping the scope
// requirement isn't the same as skipping the ability-to-pay check).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// newCircleHubWithBalance creates a circle_hub app scoped to circle_wallet
// only (no pay_invoice permission row at all) with a settled incoming
// balance of balanceMloki.
func newCircleHubWithBalance(t *testing.T, dbConn *tests.TestService, balanceMloki uint64) *db.App {
	t.Helper()
	hub := &db.App{Name: "circle-hub", Kind: db.AppKindCircleHub}
	require.NoError(t, dbConn.DB.Create(hub).Error)

	perm := &db.AppPermission{
		AppId: hub.ID,
		App:   *hub,
		Scope: constants.CIRCLE_WALLET_SCOPE,
	}
	require.NoError(t, dbConn.DB.Create(perm).Error)

	if balanceMloki > 0 {
		income := db.Transaction{
			AppId:       &hub.ID,
			State:       constants.TRANSACTION_STATE_SETTLED,
			Type:        constants.TRANSACTION_TYPE_INCOMING,
			AmountMloki: balanceMloki,
		}
		require.NoError(t, dbConn.DB.Create(&income).Error)
	}
	return hub
}

func TestSendPaymentSync_CircleHub_NoPayInvoiceScope_InternalTransfer_Succeeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHubWithBalance(t, svc, 500_000)

	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err = transactionsService.SendPaymentSync(
		tests.MockInvoice, nil,
		map[string]interface{}{"internal_transfer": true},
		svc.LNClient, &hub.ID, &dbRequestEvent.ID,
	)

	assert.NoError(t, err, "an admin-initiated internal transfer must be able to decrease a circle_hub's balance despite it having no pay_invoice scope")
}

func TestSendPaymentSync_CircleHub_NoPayInvoiceScope_RegularPayment_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHubWithBalance(t, svc, 500_000)

	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	// No internal_transfer flag — this is what a real NWC pay_invoice call
	// would look like reaching this layer. Must still be rejected: the fix
	// above must not accidentally open real payment capability on a
	// circle_hub's own (often shared/public) NWC connection.
	_, err = transactionsService.SendPaymentSync(
		tests.MockInvoice, nil, nil,
		svc.LNClient, &hub.ID, &dbRequestEvent.ID,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not have pay_invoice scope")
}

func TestSendPaymentSync_CircleHub_InternalTransfer_StillEnforcesRealBalance(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Funded with less than MockInvoice's amount (123,000 mloki) - skipping
	// the scope check must not also skip the isolated-balance check.
	hub := newCircleHubWithBalance(t, svc, 1_000)

	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err = transactionsService.SendPaymentSync(
		tests.MockInvoice, nil,
		map[string]interface{}{"internal_transfer": true},
		svc.LNClient, &hub.ID, &dbRequestEvent.ID,
	)

	assert.Error(t, err, "an internal transfer still cannot decrease a circle_hub below its real balance")
}
