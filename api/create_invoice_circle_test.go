package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/tests/mocks"
	"github.com/flokiorg/lokihub/transactions"
)

// The admin invoice endpoint gives a Circle Wallet the Hub-settled invoice its
// NWC make_invoice gives it, checked against its receive room: never a plain
// invoice that skips NIP-CW's checks.
func TestCreateInvoice_CircleWalletGetsTheHubSettledInvoice(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := &db.App{Name: "hub", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(hub).Error)
	_, err = tests.AddCircleHubConfig(svc, hub.ID)
	require.NoError(t, err)
	wallet, _, err := svc.AppsService.CreateApp("member", "", 1_000, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.MAKE_INVOICE_SCOPE}, db.AppKindCircleWallet, &hub.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)
	require.NoError(t, tests.SetCircleMaxReceive(svc, wallet.ID, 500_000))

	mockSvc := mocks.NewMockService(t)
	mockSvc.On("GetLNClient").Return(svc.LNClient)
	mockSvc.On("GetTransactionsService").Return(transactions.NewTransactionsService(svc.DB, svc.EventPublisher))
	theAPI := &api{db: svc.DB, svc: mockSvc, cfg: svc.Cfg}

	resp, err := theAPI.CreateInvoice(context.TODO(), &MakeInvoiceRequest{Amount: 100_000, AppId: &wallet.ID})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Len(t, svc.LNClient.(*tests.MockLn).MakeHoldInvoiceCalls(), 1, "a Hub-settled hold invoice")

	_, err = theAPI.CreateInvoice(context.TODO(), &MakeInvoiceRequest{Amount: 900_000, AppId: &wallet.ID})
	require.Error(t, err, "over the receive room")
}
