package transactions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CW §Own Funds and Caps: the Hub never pays a routing fee above the
// reserve it held, for a Lightning invoice or a keysend alike. 123,000 mloki at
// 20,000 ppm is 2,460; the floor 3,000 wins, below the node's own 10,000.
func circleWithReserve(t *testing.T, svc *tests.TestService) *db.App {
	t.Helper()
	hub := newCircleHub(t, svc, 0)
	require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", hub.ID).
		Updates(map[string]interface{}{"fee_reserve_ppm": 20_000, "fee_reserve_min_mloki": 3_000}).Error)
	wallet := newCircleWallet(t, svc, hub, 0, constants.BUDGET_RENEWAL_NEVER)
	tests.FundApp(svc, wallet.ID, 200_000, tests.RandomHex32())
	return wallet
}

func TestCircleRouteCap_InvoiceIsTheAnnouncedReserve(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	wallet := circleWithReserve(t, svc)

	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err = ts.SendPaymentSync(tests.MockInvoice, nil, nil, svc.LNClient, &wallet.ID, nil)
	require.NoError(t, err)
	calls := svc.LNClient.(*tests.MockLn).SendPaymentSyncCalls()
	require.NotEmpty(t, calls)
	require.NotNil(t, calls[len(calls)-1].FeeLimitMloki)
	assert.Equal(t, uint64(3_000), *calls[len(calls)-1].FeeLimitMloki)
}

func TestCircleRouteCap_KeysendIsTheAnnouncedReserve(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	wallet := circleWithReserve(t, svc)

	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err = ts.SendKeysend(123_000, "02"+tests.RandomHex32(), nil, "", svc.LNClient, &wallet.ID, nil)
	require.NoError(t, err)
	calls := svc.LNClient.(*tests.MockLn).SendKeysendCalls()
	require.NotEmpty(t, calls)
	require.NotNil(t, calls[len(calls)-1].FeeLimitMloki, "bounded by the reserve, not the node's default")
	assert.Equal(t, uint64(3_000), *calls[len(calls)-1].FeeLimitMloki)
}

func TestCircleRouteCap_OtherAppsKeysendKeepsTheNodesBound(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	app.Kind = db.AppKindIsolated
	require.NoError(t, svc.DB.Save(app).Error)
	require.NoError(t, svc.DB.Create(&db.AppPermission{AppId: app.ID, App: *app, Scope: constants.PAY_INVOICE_SCOPE}).Error)
	tests.FundApp(svc, app.ID, 200_000, tests.RandomHex32())

	ts := NewTransactionsService(svc.DB, svc.EventPublisher)
	_, err = ts.SendKeysend(123_000, "02"+tests.RandomHex32(), nil, "", svc.LNClient, &app.ID, nil)
	require.NoError(t, err)
	calls := svc.LNClient.(*tests.MockLn).SendKeysendCalls()
	require.NotEmpty(t, calls)
	assert.Nil(t, calls[len(calls)-1].FeeLimitMloki)
}
