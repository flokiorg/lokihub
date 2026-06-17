package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/flokiorg/lokihub/tests/mocks"
	"github.com/flokiorg/lokihub/transactions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// stubTransactionsService implements transactions.TransactionsService.
// Only SendPaymentSync is testify-mocked; other methods panic if called.
type stubTransactionsService struct {
	mock.Mock
}

func (s *stubTransactionsService) ConsumeEvent(_ context.Context, _ *events.Event, _ map[string]interface{}) {
}

func (s *stubTransactionsService) MakeInvoice(
	_ context.Context, _ uint64, _, _ string, _ uint64, _ map[string]interface{},
	_ lnclient.LNClient, _ *uint, _ *uint, _ *string, _ *string, _ *uint16, _ *uint64, _ *uint32,
) (*transactions.Transaction, error) {
	panic("MakeInvoice: unexpected call in test")
}

func (s *stubTransactionsService) LookupTransaction(_ context.Context, _ string, _ *string, _ lnclient.LNClient, _ *uint) (*transactions.Transaction, error) {
	panic("LookupTransaction: unexpected call in test")
}

func (s *stubTransactionsService) ListTransactions(_ context.Context, _, _, _, _ uint64, _, _ bool, _ *string, _ lnclient.LNClient, _ *uint, _ bool) ([]transactions.Transaction, uint64, error) {
	panic("ListTransactions: unexpected call in test")
}

func (s *stubTransactionsService) SendPaymentSync(payReq string, amountMloki *uint64, metadata map[string]interface{}, lnClient lnclient.LNClient, appId *uint, requestEventId *uint) (*transactions.Transaction, error) {
	args := s.Called(payReq, amountMloki, metadata, lnClient, appId, requestEventId)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*transactions.Transaction), args.Error(1)
}

func (s *stubTransactionsService) SendKeysend(_ uint64, _ string, _ []lnclient.TLVRecord, _ string, _ lnclient.LNClient, _ *uint, _ *uint) (*transactions.Transaction, error) {
	panic("SendKeysend: unexpected call in test")
}

func (s *stubTransactionsService) MakeHoldInvoice(_ context.Context, _ uint64, _, _ string, _ uint64, _ string, _ map[string]interface{}, _ lnclient.LNClient, _ *uint, _ *uint) (*transactions.Transaction, error) {
	panic("MakeHoldInvoice: unexpected call in test")
}

func (s *stubTransactionsService) SettleHoldInvoice(_ context.Context, _ string, _ lnclient.LNClient) (*transactions.Transaction, error) {
	panic("SettleHoldInvoice: unexpected call in test")
}

func (s *stubTransactionsService) CancelHoldInvoice(_ context.Context, _ string, _ lnclient.LNClient) error {
	panic("CancelHoldInvoice: unexpected call in test")
}

func (s *stubTransactionsService) SetTransactionMetadata(_ context.Context, _ uint, _ map[string]interface{}) error {
	panic("SetTransactionMetadata: unexpected call in test")
}

func (s *stubTransactionsService) SetLiquidityManager(_ *manager.LiquidityManager) {}

func (s *stubTransactionsService) EstimateFee(_ string) (uint64, error) {
	panic("EstimateFee: unexpected call in test")
}

func makeSettledTransaction(preimage string) *db.Transaction {
	now := time.Now()
	return &db.Transaction{
		PaymentHash: "abc123",
		Preimage:    &preimage,
		SettledAt:   &now,
	}
}

func TestSendPayment_WithAppId_PassedToSendPaymentSync(t *testing.T) {
	lnClient := mocks.NewMockLNClient(t)
	svc := mocks.NewMockService(t)
	txSvc := &stubTransactionsService{}

	appID := uint(42)
	invoice := "lnbc123..."
	preimage := "preimage_abc"
	settled := makeSettledTransaction(preimage)

	svc.On("GetLNClient").Return(lnClient)
	svc.On("GetTransactionsService").Return(txSvc)
	txSvc.On("SendPaymentSync", invoice, (*uint64)(nil), (map[string]interface{})(nil), lnClient, &appID, (*uint)(nil)).
		Return(settled, nil)

	theAPI := instantiateAPIWithService(svc)
	resp, err := theAPI.SendPayment(context.Background(), invoice, nil, &appID, nil)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, preimage, *resp.Preimage)
	txSvc.AssertExpectations(t)
}

func TestSendPayment_WithoutAppId_PassesNilToSendPaymentSync(t *testing.T) {
	lnClient := mocks.NewMockLNClient(t)
	svc := mocks.NewMockService(t)
	txSvc := &stubTransactionsService{}

	invoice := "lnbc456..."
	preimage := "preimage_xyz"
	settled := makeSettledTransaction(preimage)

	svc.On("GetLNClient").Return(lnClient)
	svc.On("GetTransactionsService").Return(txSvc)
	txSvc.On("SendPaymentSync", invoice, (*uint64)(nil), (map[string]interface{})(nil), lnClient, (*uint)(nil), (*uint)(nil)).
		Return(settled, nil)

	theAPI := instantiateAPIWithService(svc)
	resp, err := theAPI.SendPayment(context.Background(), invoice, nil, nil, nil)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, preimage, *resp.Preimage)
	txSvc.AssertExpectations(t)
}

func TestSendPayment_WithAppId_InsufficientBalance(t *testing.T) {
	lnClient := mocks.NewMockLNClient(t)
	svc := mocks.NewMockService(t)
	txSvc := &stubTransactionsService{}

	appID := uint(42)
	invoice := "lnbc789..."

	svc.On("GetLNClient").Return(lnClient)
	svc.On("GetTransactionsService").Return(txSvc)
	txSvc.On("SendPaymentSync", invoice, (*uint64)(nil), (map[string]interface{})(nil), lnClient, &appID, (*uint)(nil)).
		Return(nil, errors.New("insufficient balance"))

	theAPI := instantiateAPIWithService(svc)
	resp, err := theAPI.SendPayment(context.Background(), invoice, nil, &appID, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient balance")
	assert.Nil(t, resp)
	txSvc.AssertExpectations(t)
}

func TestSendPayment_LNClientNotStarted(t *testing.T) {
	svc := mocks.NewMockService(t)
	svc.On("GetLNClient").Return(nil)

	theAPI := instantiateAPIWithService(svc)
	resp, err := theAPI.SendPayment(context.Background(), "lnbc...", nil, nil, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "LNClient not started")
	assert.Nil(t, resp)
}
