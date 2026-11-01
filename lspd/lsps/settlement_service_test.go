package lsps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/invoicesrpc"
	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/chain"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/history"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockLightningClient for testing
type MockLightningClient struct {
	mock.Mock
}

func (m *MockLightningClient) GetInfo() (*lightning.GetInfoResult, error) {
	args := m.Called()
	return args.Get(0).(*lightning.GetInfoResult), args.Error(1)
}

func (m *MockLightningClient) GetBlockHeight() (uint32, error) {
	args := m.Called()
	return args.Get(0).(uint32), args.Error(1)
}

func (m *MockLightningClient) ListPeers() ([]*lightning.Peer, error) {
	args := m.Called()
	return args.Get(0).([]*lightning.Peer), args.Error(1)
}

func (m *MockLightningClient) IsConnected(destination []byte) (bool, error) {

	args := m.Called(destination)
	return args.Bool(0), args.Error(1)
}

func (m *MockLightningClient) OpenChannel(req *lightning.OpenChannelRequest) (*wire.OutPoint, error) {
	args := m.Called(req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*wire.OutPoint), args.Error(1)
}

func (m *MockLightningClient) UpdateChannelPolicy(channelPoint wire.OutPoint, policy *lightning.RoutingPolicy) error {
	args := m.Called(channelPoint, policy)
	return args.Error(0)
}

func (m *MockLightningClient) IsChannelPolicyApplied(scid uint64, peerPubkey []byte, timeLockDelta uint32) (bool, error) {
	args := m.Called(scid, peerPubkey, timeLockDelta)
	return args.Bool(0), args.Error(1)
}

func (m *MockLightningClient) GetChannel(peerID []byte, channelPoint wire.OutPoint) (*lightning.GetChannelResult, error) {
	args := m.Called(peerID, channelPoint)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*lightning.GetChannelResult), args.Error(1)
}

func (m *MockLightningClient) WaitChannelPointActive(peerID []byte, channelPoint wire.OutPoint, deadline time.Time) (*lightning.GetChannelResult, error) {
	args := m.Called(peerID, channelPoint, deadline)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*lightning.GetChannelResult), args.Error(1)
}

func (m *MockLightningClient) GetPeerId(scid *lightning.ShortChannelID) ([]byte, error) {
	args := m.Called(scid)
	return args.Get(0).([]byte), args.Error(1)
}

func (m *MockLightningClient) GetClosedChannels(nodeID string, channelPoints map[string]uint64) (map[string]uint64, error) {
	args := m.Called(nodeID, channelPoints)
	return args.Get(0).(map[string]uint64), args.Error(1)
}

func (m *MockLightningClient) WaitOnline(peerID []byte, deadline time.Time) error {
	args := m.Called(peerID, deadline)
	return args.Error(0)
}

func (m *MockLightningClient) WaitChannelActive(peerID []byte, deadline time.Time) error {
	args := m.Called(peerID, deadline)
	return args.Error(0)
}

func (m *MockLightningClient) ListChannels() ([]*lightning.Channel, error) {
	args := m.Called()
	return args.Get(0).([]*lightning.Channel), args.Error(1)
}

func (m *MockLightningClient) SubscribeInvoices() (<-chan *lightning.InvoiceUpdate, <-chan error, error) {
	args := m.Called()
	return args.Get(0).(<-chan *lightning.InvoiceUpdate), args.Get(1).(<-chan error), args.Error(2)
}

func (m *MockLightningClient) SubscribeSingleInvoice(ctx context.Context, paymentHash []byte) (<-chan *lightning.InvoiceUpdate, <-chan error, error) {
	args := m.Called(ctx, paymentHash)
	return args.Get(0).(<-chan *lightning.InvoiceUpdate), args.Get(1).(<-chan error), args.Error(2)
}

func (m *MockLightningClient) SendCustomMessage(ctx context.Context, msg *lnrpc.SendCustomMessageRequest) (*lnrpc.SendCustomMessageResponse, error) {
	args := m.Called(ctx, msg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*lnrpc.SendCustomMessageResponse), args.Error(1)
}

func (m *MockLightningClient) SubscribeCustomMessages(ctx context.Context, req *lnrpc.SubscribeCustomMessagesRequest) (lnrpc.Lightning_SubscribeCustomMessagesClient, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(lnrpc.Lightning_SubscribeCustomMessagesClient), args.Error(1)
}

func (m *MockLightningClient) GetNodeInfo(pubkey string) (*lightning.NodeInfo, error) {
	args := m.Called(pubkey)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*lightning.NodeInfo), args.Error(1)
}

func (m *MockLightningClient) LookupInvoiceState(ctx context.Context, paymentRequest string) (lightning.InvoiceState, error) {
	args := m.Called(ctx, paymentRequest)
	return args.Get(0).(lightning.InvoiceState), args.Error(1)
}

func (m *MockLightningClient) LookupInvoiceStateByHash(ctx context.Context, paymentHash []byte) (lightning.InvoiceState, error) {
	args := m.Called(ctx, paymentHash)
	return args.Get(0).(lightning.InvoiceState), args.Error(1)
}

func (m *MockLightningClient) AddHoldInvoice(ctx context.Context, req *invoicesrpc.AddHoldInvoiceRequest) (*invoicesrpc.AddHoldInvoiceResp, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*invoicesrpc.AddHoldInvoiceResp), args.Error(1)
}

func (m *MockLightningClient) SettleInvoice(ctx context.Context, preimage []byte) error {
	args := m.Called(ctx, preimage)
	return args.Error(0)
}

func (m *MockLightningClient) CancelInvoice(ctx context.Context, paymentHash []byte) error {
	args := m.Called(ctx, paymentHash)
	return args.Error(0)
}

// MockOpeningStore for testing
type MockOpeningStore struct {
	mock.Mock
}

func (m *MockOpeningStore) GetFeeParamsSettings(token string) ([]*core.OpeningFeeParamsSetting, error) {
	args := m.Called(token)
	return args.Get(0).([]*core.OpeningFeeParamsSetting), args.Error(1)
}

func (m *MockOpeningStore) SetFeeParams(token string, validity time.Duration, params core.OpeningFeeParams) error {
	args := m.Called(token, validity, params)
	return args.Error(0)
}

func (m *MockOpeningStore) RegisterLsps2Buy(reg *core.Lsps2BuyRegistration) error {
	args := m.Called(reg)
	return args.Error(0)
}

func (m *MockOpeningStore) GetLsps2BuyRegistration(scid uint64) (*core.Lsps2BuyRegistration, error) {
	args := m.Called(scid)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.Lsps2BuyRegistration), args.Error(1)
}

func (m *MockOpeningStore) RegisterLsps1Order(order *core.Lsps1Order) error {
	args := m.Called(order)
	return args.Error(0)
}

func (m *MockOpeningStore) GetLsps1Order(orderId string) (*core.Lsps1Order, error) {
	args := m.Called(orderId)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningStore) GetLsps1OrderByPaymentInvoice(paymentInvoice string) (*core.Lsps1Order, error) {
	args := m.Called(paymentInvoice)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningStore) UpdateLsps1OrderState(orderId string, newState string, newPaymentState string, expectedCurrentState string) (bool, error) {
	args := m.Called(orderId, newState, newPaymentState, expectedCurrentState)
	return args.Bool(0), args.Error(1)
}

func (m *MockOpeningStore) RegisterLsps5Webhook(webhook *core.Lsps5Webhook) error {
	args := m.Called(webhook)
	return args.Error(0)
}

func (m *MockOpeningStore) GetLsps5Webhooks(clientNodeId string) ([]*core.Lsps5Webhook, error) {
	args := m.Called(clientNodeId)
	return args.Get(0).([]*core.Lsps5Webhook), args.Error(1)
}

func (m *MockOpeningStore) RemoveLsps5Webhook(clientNodeId, appName string) error {
	args := m.Called(clientNodeId, appName)
	return args.Error(0)
}

func (m *MockOpeningStore) CountLsps1Orders(clientNodeId string, state string, excludeExpiredBefore *time.Time) (int, error) {
	args := m.Called(clientNodeId, state, excludeExpiredBefore)
	return args.Int(0), args.Error(1)
}

func (m *MockOpeningStore) CountLsps2PendingRegistrations(peerId string) (int, error) {
	args := m.Called(peerId)
	return args.Int(0), args.Error(1)
}

func (m *MockOpeningStore) CountLsps5Webhooks(clientNodeId string) (int, error) {
	args := m.Called(clientNodeId)
	return args.Int(0), args.Error(1)
}

func (m *MockOpeningStore) GetPaidLsps1Order(clientNodeId string) (*core.Lsps1Order, error) {
	args := m.Called(clientNodeId)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningStore) GetPaidLsps1Orders(clientNodeId string) ([]*core.Lsps1Order, error) {
	args := m.Called(clientNodeId)
	return args.Get(0).([]*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningStore) GetAllPaidLsps1Orders() ([]*core.Lsps1Order, error) {
	args := m.Called()
	return args.Get(0).([]*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningStore) ListLsps1Orders(limit, offset int) ([]*core.Lsps1Order, error) {
	args := m.Called(limit, offset)
	return args.Get(0).([]*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningStore) GetAllCreatedLsps1Orders() ([]*core.Lsps1Order, error) {
	args := m.Called()
	return args.Get(0).([]*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningStore) UpdateLsps1OrderFunding(orderId string, fundingTxid string, fundingOutpoint int) (bool, error) {
	args := m.Called(orderId, fundingTxid, fundingOutpoint)
	return args.Bool(0), args.Error(1)
}

func (m *MockOpeningStore) GetFeeParamsMenu(token string, privateKey *btcec.PrivateKey) ([]*core.OpeningFeeParams, error) {
	args := m.Called(token, privateKey)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*core.OpeningFeeParams), args.Error(1)
}

func (m *MockOpeningStore) ValidateOpeningFeeParams(params *core.OpeningFeeParams, publicKey *btcec.PublicKey) bool {
	args := m.Called(params, publicKey)
	return args.Bool(0)
}

func (m *MockOpeningStore) IsCurrentChainFeeCheaper(currentFeeratePerVByte *float64, referenceFeeratePerVByte float64) bool {
	args := m.Called(currentFeeratePerVByte, referenceFeeratePerVByte)
	return args.Bool(0)
}

func (m *MockOpeningStore) GetReservedLiquidity() (uint64, error) {
	args := m.Called()
	return args.Get(0).(uint64), args.Error(1)
}

func (m *MockOpeningStore) ExpireStaleOrders() (int64, error) {
	args := m.Called()
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockOpeningStore) ExpireStaleLsps2Registrations() (int64, error) {
	args := m.Called()
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockOpeningStore) ReserveLsps1Order(order *core.Lsps1Order, availableWalletBalanceLoki uint64) (bool, error) {
	args := m.Called(order, availableWalletBalanceLoki)
	return args.Bool(0), args.Error(1)
}

func (m *MockOpeningStore) ReserveLsps2Buy(reg *core.Lsps2BuyRegistration, availableWalletBalanceLoki uint64) (bool, error) {
	args := m.Called(reg, availableWalletBalanceLoki)
	return args.Bool(0), args.Error(1)
}

func (m *MockLightningClient) WalletBalance(ctx context.Context) (*lightning.WalletBalanceResult, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*lightning.WalletBalanceResult), args.Error(1)
}

func (m *MockLightningClient) QueryRoutes(destination []byte, amountMloki uint64, outgoingChanId uint64) ([]*lightning.Route, error) {
	args := m.Called(destination, amountMloki)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*lightning.Route), args.Error(1)
}

func (m *MockLightningClient) IsChannelRoutable(scid uint64) (bool, error) {
	args := m.Called(scid)
	return args.Bool(0), args.Error(1)
}

// MockHistoryStore for testing
type MockHistoryStore struct {
	mock.Mock
}

func (m *MockHistoryStore) UpdateChannels(ctx context.Context, updates []*history.ChannelUpdate) error {
	args := m.Called(ctx, updates)
	return args.Error(0)
}

func (m *MockHistoryStore) InsertForwards(ctx context.Context, forwards []*history.Forward, nodeId []byte) error {
	args := m.Called(ctx, forwards, nodeId)
	return args.Error(0)
}

func (m *MockHistoryStore) UpdateForwards(ctx context.Context, forwards []*history.Forward, nodeId []byte) error {
	args := m.Called(ctx, forwards, nodeId)
	return args.Error(0)
}

func (m *MockHistoryStore) FetchLndForwardOffset(ctx context.Context, nodeId []byte) (*time.Time, error) {
	args := m.Called(ctx, nodeId)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*time.Time), args.Error(1)
}

func (m *MockHistoryStore) AddOpenChannelHtlc(ctx context.Context, htlc *history.OpenChannelHtlc) error {
	args := m.Called(ctx, htlc)
	return args.Error(0)
}

// MockFeeEstimator for testing
type MockFeeEstimator struct {
	mock.Mock
}

func (m *MockFeeEstimator) EstimateFeeRate(ctx context.Context, strategy chain.FeeStrategy) (*chain.FeeEstimation, error) {
	args := m.Called(ctx, strategy)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*chain.FeeEstimation), args.Error(1)
}

// TestProcessSettledInvoice_Success tests happy path invoice settlement
func TestProcessSettledInvoice_Success(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)

	invoice := "lnbc1test..."
	order := &core.Lsps1Order{
		OrderId:           "test-order-123",
		PeerId:            "deadbeef",
		OrderState:        "CREATED",
		PaymentState:      "CREATED",
		ClientBalanceLoki: 1000000,
	}

	mockStore.On("GetLsps1OrderByPaymentInvoice", invoice).Return(order, nil)
	mockStore.On("UpdateLsps1OrderState", "test-order-123", "PAID", "PAID", "CREATED").Return(true, nil)

	// Mock connectivity check to return false, so we stop after PAID notification
	// "deadbeef" decoded is [222 173 190 239]
	peerIdBytes, _ := hex.DecodeString("deadbeef")
	mockClient.On("IsConnected", peerIdBytes).Return(false, nil)

	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{}

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	// Expect notification
	mockNotification.On("NotifyOrderStateChanged", "deadbeef", "test-order-123", "PAID", "", "").Return(true, nil)

	update := &lightning.InvoiceUpdate{
		State:          lightning.InvoiceStateSettled,
		PaymentRequest: invoice,
		AmountMloki:    1000000,
	}

	svc.processSettledInvoice(update)

	// Wait for goroutines (notification is async)
	time.Sleep(50 * time.Millisecond)

	mockStore.AssertExpectations(t)
	mockNotification.AssertExpectations(t)
}

func (m *MockLightningClient) AddInvoice(ctx context.Context, invoice *lnrpc.Invoice) (*lnrpc.AddInvoiceResponse, error) {
	args := m.Called(ctx, invoice)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*lnrpc.AddInvoiceResponse), args.Error(1)
}

// TestProcessSettledInvoice_OrderNotFound tests non-LSPS1 invoice
func TestProcessSettledInvoice_OrderNotFound(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)

	invoice := "lnbc1test..."

	mockStore.On("GetLsps1OrderByPaymentInvoice", invoice).Return(nil, assert.AnError)

	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	conf := &config.NodeConfig{}

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, nil)

	update := &lightning.InvoiceUpdate{
		State:          lightning.InvoiceStateSettled,
		PaymentRequest: invoice,
	}

	svc.processSettledInvoice(update)

	// Should not call UpdateLsps1OrderState
	mockStore.AssertNotCalled(t, "UpdateLsps1OrderState", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestProcessSettledInvoice_TriggerChannelOpen tests that channel opening is triggered
func TestProcessSettledInvoice_TriggerChannelOpen(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	conf := &config.NodeConfig{
		FeeRate:       0.000001,
		BaseFeeMloki:  1000,
		TimeLockDelta: 40,
	}

	invoice := "lnbc1test..."
	order := &core.Lsps1Order{
		OrderId:           "test-order-123",
		PeerId:            "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f", // Valid hex for peer ID
		OrderState:        "CREATED",
		PaymentState:      "CREATED",
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
		AnnounceChannel:   false,
	}

	peerId, _ := hex.DecodeString(order.PeerId)

	mockStore.On("GetLsps1OrderByPaymentInvoice", invoice).Return(order, nil)
	mockStore.On("UpdateLsps1OrderState", "test-order-123", "PAID", "PAID", "CREATED").Return(true, nil)

	// Connectivity check
	mockClient.On("IsConnected", peerId).Return(true, nil)

	// Fee estimation
	feeEst := &chain.FeeEstimation{
		LokiPerVByte: func() *float64 { f := 1.0; return &f }(),
	}
	feeEstimator.On("EstimateFeeRate", mock.Anything, chain.FeeStrategyEconomy).Return(feeEst, nil)

	// Channel opening
	chanPoint := &wire.OutPoint{
		Hash: func() chainhash.Hash {
			h, _ := chainhash.NewHashFromStr("deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
			return *h
		}(),
		Index: 0,
	}
	mockClient.On("OpenChannel", mock.MatchedBy(func(req *lightning.OpenChannelRequest) bool {
		return req.CapacityLoki == 6000000 && req.PushLoki == 1000000
	})).Return(chanPoint, nil)

	// Store updates
	mockStore.On("UpdateLsps1OrderFunding", "test-order-123", chanPoint.Hash.String(), 0).Return(true, nil)
	mockStore.On("UpdateLsps1OrderState", "test-order-123", "COMPLETED", "PAID", "PAID").Return(true, nil)

	mockNotification := new(MockNotificationService)

	// Expect notifications
	// 1. PAID
	mockNotification.On("NotifyOrderStateChanged", order.PeerId, "test-order-123", "PAID", "", "").Return(true, nil)
	// 2. COMPLETED
	mockNotification.On("NotifyOrderStateChanged", order.PeerId, "test-order-123", "COMPLETED", chanPoint.String(), "").Return(true, nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	update := &lightning.InvoiceUpdate{
		State:          lightning.InvoiceStateSettled,
		PaymentRequest: invoice,
		AmountMloki:    1000000,
	}

	svc.processSettledInvoice(update)

	// Wait for goroutine to finish (simple sleep for now as it's a unit test)
	time.Sleep(100 * time.Millisecond)

	mockStore.AssertExpectations(t)
	mockClient.AssertExpectations(t)
	mockNotification.AssertExpectations(t)
}

// TestProcessSettledInvoice_AlreadyPaid tests duplicate settlement
func TestProcessSettledInvoice_AlreadyPaid(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)

	invoice := "lnbc1test..."
	order := &core.Lsps1Order{
		OrderId:      "test-order-123",
		OrderState:   "PAID", // Already paid
		PaymentState: "PAID",
	}

	mockStore.On("GetLsps1OrderByPaymentInvoice", invoice).Return(order, nil)

	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	conf := &config.NodeConfig{}

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, nil)

	update := &lightning.InvoiceUpdate{
		State:          lightning.InvoiceStateSettled,
		PaymentRequest: invoice,
	}

	svc.processSettledInvoice(update)

	// Should not call UpdateLsps1OrderState for already paid order
	mockStore.AssertNotCalled(t, "UpdateLsps1OrderState", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestGracefulShutdown tests context cancellation
func TestGracefulShutdown(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)

	invoiceChan := make(chan *lightning.InvoiceUpdate, 1)
	errChan := make(chan error, 1)

	mockClient.On("SubscribeInvoices").Return((<-chan *lightning.InvoiceUpdate)(invoiceChan), (<-chan error)(errChan), nil)
	mockStore.On("GetAllPaidLsps1Orders").Return([]*core.Lsps1Order{}, nil)
	mockStore.On("GetAllCreatedLsps1Orders").Return([]*core.Lsps1Order{}, nil)

	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	conf := &config.NodeConfig{}

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, nil)

	ctx, cancel := context.WithCancel(context.Background())
	err := svc.Start(ctx)

	assert.NoError(t, err)

	// Let it run briefly
	time.Sleep(100 * time.Millisecond)

	// Cancel and stop
	cancel()
	svc.Stop()

	// Give it a moment to shutdown
	time.Sleep(100 * time.Millisecond)

	mockClient.AssertExpectations(t)
}

// TestLsps1SettlementService_StartupRecovery tests that PAID but unfulfilled orders are processed on startup
func TestLsps1SettlementService_StartupRecovery(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{}

	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	staleOrder := &core.Lsps1Order{
		OrderId:           "stale-order-001",
		PeerId:            peerPubkey,
		OrderState:        "PAID",
		PaymentState:      "PAID",
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
	}

	// 1. Return the stale order during recovery check; no CREATED orders with settled invoices
	mockStore.On("GetAllPaidLsps1Orders").Return([]*core.Lsps1Order{staleOrder}, nil)
	mockStore.On("GetAllCreatedLsps1Orders").Return([]*core.Lsps1Order{}, nil)

	// 2. Mock the channel opening process
	mockClient.On("IsConnected", peerIdBytes).Return(true, nil)
	feeEst := &chain.FeeEstimation{LokiPerVByte: func() *float64 { f := 1.0; return &f }()}
	feeEstimator.On("EstimateFeeRate", mock.Anything, chain.FeeStrategyEconomy).Return(feeEst, nil)

	chanPoint := &wire.OutPoint{
		Hash:  chainhash.Hash{0xde, 0xad, 0xbe, 0xef},
		Index: 0,
	}
	mockClient.On("OpenChannel", mock.Anything).Return(chanPoint, nil)
	mockStore.On("UpdateLsps1OrderFunding", "stale-order-001", chanPoint.Hash.String(), 0).Return(true, nil)
	mockStore.On("UpdateLsps1OrderState", "stale-order-001", "COMPLETED", "PAID", "PAID").Return(true, nil)
	mockNotification.On("NotifyOrderStateChanged", peerPubkey, "stale-order-001", "COMPLETED", chanPoint.String(), "").Return(true, nil)

	invoiceChan := make(chan *lightning.InvoiceUpdate, 1)
	errChan := make(chan error, 1)

	mockClient.On("SubscribeInvoices").Return((<-chan *lightning.InvoiceUpdate)(invoiceChan), (<-chan error)(errChan), nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := svc.Start(ctx)
	assert.NoError(t, err)

	// Wait for startup recovery goroutine
	time.Sleep(200 * time.Millisecond)

	svc.Stop()

	mockStore.AssertCalled(t, "GetAllPaidLsps1Orders")
	mockStore.AssertCalled(t, "UpdateLsps1OrderState", "stale-order-001", "COMPLETED", "PAID", "PAID")
	mockNotification.AssertExpectations(t)
}

// raceTestStore wraps MockOpeningStore but implements UpdateLsps1OrderState
// with real, mutex-protected compare-and-set semantics instead of a canned
// mock response, so two goroutines racing for the same order genuinely
// only let one through — proving the F-1 fix holds at the service layer,
// not just that a mock was configured a certain way.
type raceTestStore struct {
	*MockOpeningStore
	mu    sync.Mutex
	state map[string]string // orderId -> current order_state
}

func newRaceTestStore(orderId, initialState string) *raceTestStore {
	return &raceTestStore{
		MockOpeningStore: new(MockOpeningStore),
		state:            map[string]string{orderId: initialState},
	}
}

func (s *raceTestStore) UpdateLsps1OrderState(orderId, newState, newPaymentState, expectedCurrentState string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state[orderId] != expectedCurrentState {
		return false, nil
	}
	s.state[orderId] = newState
	return true, nil
}

// TestF1_ConcurrentSettlementAndRecoveryOpenExactlyOneChannel reproduces
// the F-1 race: a live SubscribeInvoices settlement event and the startup
// recovery sweep can both observe the same order as CREATED and race to
// mark it PAID. Before the fix, both paths would call openChannelForOrder,
// double-opening a channel. After the fix, only the CAS winner proceeds.
func TestF1_ConcurrentSettlementAndRecoveryOpenExactlyOneChannel(t *testing.T) {
	const orderId = "race-order-1"
	const invoice = "lnbc1race..."
	const peerId = "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"

	mockStore := newRaceTestStore(orderId, "CREATED")
	// Both racing paths look up the SAME order (with OrderState still
	// CREATED in the returned struct, matching what a real DB read would
	// return before either writer has moved it) — this is the stale read
	// that made the race possible pre-fix.
	order := &core.Lsps1Order{
		OrderId:           orderId,
		PeerId:            peerId,
		OrderState:        "CREATED",
		PaymentState:      "CREATED",
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
	}
	mockStore.On("GetLsps1OrderByPaymentInvoice", invoice).Return(order, nil)

	peerIdBytes, _ := hex.DecodeString(peerId)
	mockClient := new(MockLightningClient)
	mockClient.On("IsConnected", peerIdBytes).Return(true, nil)

	feeEstimator := new(MockFeeEstimator)
	f := 1.0
	feeEstimator.On("EstimateFeeRate", mock.Anything, mock.Anything).Return(&chain.FeeEstimation{LokiPerVByte: &f}, nil)

	chanPoint := &wire.OutPoint{Hash: chainhash.Hash{0xf1}, Index: 0}
	mockClient.On("OpenChannel", mock.Anything).Return(chanPoint, nil)
	mockStore.On("UpdateLsps1OrderFunding", orderId, mock.Anything, mock.Anything).Return(true, nil)

	historyStore := new(MockHistoryStore)
	conf := &config.NodeConfig{FeeRate: 0.000001, BaseFeeMloki: 1000, TimeLockDelta: 40}

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// The live settlement-event path.
		svc.processSettledInvoice(&lightning.InvoiceUpdate{
			State:          lightning.InvoiceStateSettled,
			PaymentRequest: invoice,
			AmountMloki:    1000000,
		})
	}()
	go func() {
		defer wg.Done()
		// The startup-recovery path, racing for the same order.
		svc.processSettledOrder(order)
	}()
	wg.Wait()

	// Give the winner's async openChannelForOrder goroutine time to finish.
	time.Sleep(100 * time.Millisecond)

	mockClient.AssertNumberOfCalls(t, "OpenChannel", 1)

	mockStore.mu.Lock()
	finalState := mockStore.state[orderId]
	mockStore.mu.Unlock()
	assert.Equal(t, "COMPLETED", finalState, "order should end up COMPLETED via exactly one winner, not stuck or double-processed")
}

// testHeldOrderPreimage returns a fixed 32-byte preimage (hex-encoded, as
// stored on core.Lsps1Order) plus its raw bytes and sha256 payment hash, for
// tests exercising the hold-invoice settle/cancel paths.
func testHeldOrderPreimage() (preimageHex string, preimageBytes []byte, hash [32]byte) {
	preimageBytes = []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
		0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
	}
	return hex.EncodeToString(preimageBytes), preimageBytes, sha256.Sum256(preimageBytes)
}

// TestProcessAcceptedInvoice_PeerNotConnected_CancelsHeldOrder covers the
// key behavioral difference from the legacy (settled-invoice) flow: a
// disconnected peer can't be silently deferred once the client's HTLC is
// held, since every second holds real force-close risk. It must cancel
// (instant refund) instead.
func TestProcessAcceptedInvoice_PeerNotConnected_CancelsHeldOrder(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{}

	preimageHex, _, hash := testHeldOrderPreimage()
	invoice := "lnbc1holdtest..."
	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	order := &core.Lsps1Order{
		OrderId:           "held-order-1",
		PeerId:            peerPubkey,
		OrderState:        "CREATED",
		PaymentState:      "EXPECT_PAYMENT",
		PaymentInvoice:    invoice,
		PaymentPreimage:   &preimageHex,
		ClientBalanceLoki: 1000000,
	}

	mockStore.On("GetLsps1OrderByPaymentInvoice", invoice).Return(order, nil)
	// processAcceptedOrder's CAS: CREATED -> PAID/HOLD (committed to attempt, not yet settled)
	mockStore.On("UpdateLsps1OrderState", "held-order-1", "PAID", "HOLD", "CREATED").Return(true, nil)
	mockNotification.On("NotifyOrderStateChanged", peerPubkey, "held-order-1", "PAID", "", "").Return(true, nil)

	// openChannelForHeldOrder: peer unreachable -> fail fast and cancel,
	// unlike the legacy flow's silent "defer, will be handled by JIT or manual".
	mockClient.On("IsConnected", peerIdBytes).Return(false, nil)
	mockClient.On("CancelInvoice", mock.Anything, hash[:]).Return(nil)
	mockStore.On("UpdateLsps1OrderState", "held-order-1", "FAILED", "REFUNDED", "PAID").Return(true, nil)
	mockNotification.On("NotifyOrderStateChanged", peerPubkey, "held-order-1", "FAILED", "", mock.Anything).Return(true, nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	update := &lightning.InvoiceUpdate{
		State:          lightning.InvoiceStateAccepted,
		PaymentRequest: invoice,
		AmountMloki:    1000000,
	}

	svc.processAcceptedInvoice(update)

	// Wait for the two nested goroutines (openChannelForHeldOrder, then its
	// own notification) to finish.
	time.Sleep(150 * time.Millisecond)

	mockStore.AssertExpectations(t)
	mockClient.AssertExpectations(t)
	mockNotification.AssertExpectations(t)
}

// TestProcessAcceptedInvoice_OrderNotFound mirrors
// TestProcessSettledInvoice_OrderNotFound for the ACCEPTED path: a
// non-LSPS1 invoice's ACCEPTED event must be ignored, not acted on.
func TestProcessAcceptedInvoice_OrderNotFound(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	conf := &config.NodeConfig{}

	invoice := "lnbc1test..."
	mockStore.On("GetLsps1OrderByPaymentInvoice", invoice).Return(nil, assert.AnError)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, nil)

	update := &lightning.InvoiceUpdate{
		State:          lightning.InvoiceStateAccepted,
		PaymentRequest: invoice,
	}

	svc.processAcceptedInvoice(update)

	mockStore.AssertNotCalled(t, "UpdateLsps1OrderState", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestOpenChannelForHeldOrder_Success covers the money-critical happy path:
// once OpenChannel broadcasts the funding tx, the invoice must be settled
// (funds captured) with the order's own stored preimage before the order is
// marked COMPLETED.
func TestOpenChannelForHeldOrder_Success(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{
		FeeRate:       0.000001,
		BaseFeeMloki:  1000,
		TimeLockDelta: 4000,
	}

	preimageHex, preimageBytes, _ := testHeldOrderPreimage()
	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	order := &core.Lsps1Order{
		OrderId:           "held-order-2",
		PeerId:            peerPubkey,
		OrderState:        "PAID",
		PaymentState:      "HOLD",
		PaymentPreimage:   &preimageHex,
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
	}

	mockClient.On("IsConnected", peerIdBytes).Return(true, nil)
	feeEst := &chain.FeeEstimation{LokiPerVByte: func() *float64 { f := 1.0; return &f }()}
	feeEstimator.On("EstimateFeeRate", mock.Anything, chain.FeeStrategyEconomy).Return(feeEst, nil)

	chanPoint := &wire.OutPoint{
		Hash: chainhash.Hash{0xaa, 0xbb},
	}
	mockClient.On("OpenChannel", mock.MatchedBy(func(req *lightning.OpenChannelRequest) bool {
		return req.CapacityLoki == 6000000 && req.PushLoki == 1000000
	})).Return(chanPoint, nil)
	mockClient.On("SettleInvoice", mock.Anything, preimageBytes).Return(nil)

	mockStore.On("UpdateLsps1OrderFunding", "held-order-2", chanPoint.Hash.String(), 0).Return(true, nil)
	mockStore.On("UpdateLsps1OrderState", "held-order-2", "COMPLETED", "PAID", "PAID").Return(true, nil)
	mockNotification.On("NotifyOrderStateChanged", peerPubkey, "held-order-2", "COMPLETED", chanPoint.String(), "").Return(true, nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	svc.openChannelForHeldOrder(order)

	// NotifyOrderStateChanged fires via its own goroutine.
	time.Sleep(50 * time.Millisecond)

	mockClient.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockNotification.AssertExpectations(t)
}

// TestOpenChannelForHeldOrder_OpenChannelFails_CancelsInvoice covers the
// other terminal outcome: when OpenChannel fails outright (and there's no
// orphaned channel from an earlier crashed attempt to reconcile against),
// the held invoice must be cancelled -- an instant, on-chain-fee-free
// refund -- instead of leaving the client's HTLC hanging or the order
// waiting on the slow RefundOnchainAddress path.
func TestOpenChannelForHeldOrder_OpenChannelFails_CancelsInvoice(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{
		FeeRate:       0.000001,
		BaseFeeMloki:  1000,
		TimeLockDelta: 4000,
	}

	preimageHex, _, hash := testHeldOrderPreimage()
	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	order := &core.Lsps1Order{
		OrderId:           "held-order-3",
		PeerId:            peerPubkey,
		OrderState:        "PAID",
		PaymentState:      "HOLD",
		PaymentPreimage:   &preimageHex,
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
	}

	mockClient.On("IsConnected", peerIdBytes).Return(true, nil)
	feeEst := &chain.FeeEstimation{LokiPerVByte: func() *float64 { f := 1.0; return &f }()}
	feeEstimator.On("EstimateFeeRate", mock.Anything, chain.FeeStrategyEconomy).Return(feeEst, nil)

	mockClient.On("OpenChannel", mock.Anything).Return(nil, assert.AnError)
	// reconcileUnclaimedChannel: no existing channel to this peer, so
	// there's nothing to reconcile -- a genuine failure.
	mockClient.On("ListChannels").Return([]*lightning.Channel{}, nil)

	mockClient.On("CancelInvoice", mock.Anything, hash[:]).Return(nil)
	mockStore.On("UpdateLsps1OrderState", "held-order-3", "FAILED", "REFUNDED", "PAID").Return(true, nil)
	mockNotification.On("NotifyOrderStateChanged", peerPubkey, "held-order-3", "FAILED", "", mock.Anything).Return(true, nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	svc.openChannelForHeldOrder(order)

	// NotifyOrderStateChanged fires via its own goroutine.
	time.Sleep(50 * time.Millisecond)

	mockClient.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockNotification.AssertExpectations(t)
	mockClient.AssertNotCalled(t, "SettleInvoice", mock.Anything, mock.Anything)
}

// TestSettleHeldOrderInvoice_RetriesThenSucceeds proves settleHeldOrderInvoice
// doesn't give up on the first transient SettleInvoice error -- once the LSP
// is committed (channel already broadcast), there's no cancel option left,
// so it must retry rather than abandon the settlement.
func TestSettleHeldOrderInvoice_RetriesThenSucceeds(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	conf := &config.NodeConfig{}

	preimageHex, preimageBytes, _ := testHeldOrderPreimage()
	order := &core.Lsps1Order{
		OrderId:         "held-order-4",
		PeerId:          "deadbeef",
		PaymentPreimage: &preimageHex,
	}

	mockClient.On("SettleInvoice", mock.Anything, preimageBytes).Return(assert.AnError).Once()
	mockClient.On("SettleInvoice", mock.Anything, preimageBytes).Return(nil).Once()

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, nil)

	result := svc.settleHeldOrderInvoice(order)

	assert.True(t, result, "must report success once SettleInvoice eventually succeeds, not just retry silently")
	mockClient.AssertExpectations(t)
	mockClient.AssertNumberOfCalls(t, "SettleInvoice", 2)
}

// TestRecoverStuckOrders_HeldOrder_UsesOpenChannelForHeldOrder proves
// startup recovery routes a stuck PaymentState=HOLD order through
// openChannelForHeldOrder (settle-or-cancel), not the legacy
// openChannelForOrder (which never resolves the held HTLC at all): if
// routing were wrong, the SettleInvoice expectation below would simply
// never be met.
func TestRecoverStuckOrders_HeldOrder_UsesOpenChannelForHeldOrder(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{}

	preimageHex, preimageBytes, _ := testHeldOrderPreimage()
	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	heldOrder := &core.Lsps1Order{
		OrderId:           "held-recovery-1",
		PeerId:            peerPubkey,
		OrderState:        "PAID",
		PaymentState:      "HOLD",
		PaymentPreimage:   &preimageHex,
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
	}

	mockStore.On("GetAllPaidLsps1Orders").Return([]*core.Lsps1Order{heldOrder}, nil)
	mockStore.On("GetAllCreatedLsps1Orders").Return([]*core.Lsps1Order{}, nil)

	mockClient.On("IsConnected", peerIdBytes).Return(true, nil)
	feeEst := &chain.FeeEstimation{LokiPerVByte: func() *float64 { f := 1.0; return &f }()}
	feeEstimator.On("EstimateFeeRate", mock.Anything, chain.FeeStrategyEconomy).Return(feeEst, nil)

	chanPoint := &wire.OutPoint{Hash: chainhash.Hash{0xcc}}
	mockClient.On("OpenChannel", mock.Anything).Return(chanPoint, nil)
	mockClient.On("SettleInvoice", mock.Anything, preimageBytes).Return(nil)
	mockStore.On("UpdateLsps1OrderFunding", "held-recovery-1", chanPoint.Hash.String(), 0).Return(true, nil)
	mockStore.On("UpdateLsps1OrderState", "held-recovery-1", "COMPLETED", "PAID", "PAID").Return(true, nil)
	mockNotification.On("NotifyOrderStateChanged", peerPubkey, "held-recovery-1", "COMPLETED", chanPoint.String(), "").Return(true, nil)

	invoiceChan := make(chan *lightning.InvoiceUpdate, 1)
	errChan := make(chan error, 1)
	mockClient.On("SubscribeInvoices").Return((<-chan *lightning.InvoiceUpdate)(invoiceChan), (<-chan error)(errChan), nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := svc.Start(ctx)
	assert.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	svc.Stop()

	mockClient.AssertCalled(t, "SettleInvoice", mock.Anything, preimageBytes)
	mockStore.AssertCalled(t, "UpdateLsps1OrderState", "held-recovery-1", "COMPLETED", "PAID", "PAID")
	mockNotification.AssertExpectations(t)
}

// TestOpenChannelForHeldOrder_ReconciledChannel_SettlesInvoice covers the
// crash-recovery branch inside openChannelForHeldOrder's failure path
// (F-8-style): OpenChannel errors on this attempt, but a channel to the
// peer already exists -- broadcast by an earlier attempt that crashed
// before recording it. reconcileUnclaimedChannel finds and completes the
// order on its own; this must still settle the invoice, since the legacy
// reconcileUnclaimedChannel helper (reused unmodified here) has no idea an
// invoice needs settling.
func TestOpenChannelForHeldOrder_ReconciledChannel_SettlesInvoice(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{
		FeeRate:       0.000001,
		BaseFeeMloki:  1000,
		TimeLockDelta: 4000,
	}

	preimageHex, preimageBytes, _ := testHeldOrderPreimage()
	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	order := &core.Lsps1Order{
		OrderId:           "held-order-5",
		PeerId:            peerPubkey,
		OrderState:        "PAID",
		PaymentState:      "HOLD",
		PaymentPreimage:   &preimageHex,
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
	}

	mockClient.On("IsConnected", peerIdBytes).Return(true, nil)
	feeEst := &chain.FeeEstimation{LokiPerVByte: func() *float64 { f := 1.0; return &f }()}
	feeEstimator.On("EstimateFeeRate", mock.Anything, chain.FeeStrategyEconomy).Return(feeEst, nil)

	// This attempt's own OpenChannel call fails...
	mockClient.On("OpenChannel", mock.Anything).Return(nil, assert.AnError)

	// ...but an earlier crashed attempt's channel is already there.
	orphanPoint := &wire.OutPoint{Hash: chainhash.Hash{0xdd}, Index: 0}
	mockClient.On("ListChannels").Return([]*lightning.Channel{
		{ChannelPoint: orphanPoint, PeerId: peerIdBytes},
	}, nil)
	// claimedChannelPoints: no order has claimed this channel point yet.
	mockStore.On("ListLsps1Orders", 500, 0).Return([]*core.Lsps1Order{}, nil)

	mockStore.On("UpdateLsps1OrderFunding", "held-order-5", orphanPoint.Hash.String(), 0).Return(true, nil)
	mockStore.On("UpdateLsps1OrderState", "held-order-5", "COMPLETED", "PAID", "PAID").Return(true, nil)
	mockNotification.On("NotifyOrderStateChanged", peerPubkey, "held-order-5", "COMPLETED", orphanPoint.String(), "").Return(true, nil)

	// The point of this test: settling must happen even though this
	// attempt's own OpenChannel call errored.
	mockClient.On("SettleInvoice", mock.Anything, preimageBytes).Return(nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	svc.openChannelForHeldOrder(order)

	time.Sleep(50 * time.Millisecond)

	mockClient.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockNotification.AssertExpectations(t)
	mockClient.AssertNotCalled(t, "CancelInvoice", mock.Anything, mock.Anything)
}

// TestOpenChannelForHeldOrder_SettleInvoiceFailsPermanently_DoesNotMarkCompleted
// is the regression test for H-2: OpenChannel succeeds (a real on-chain
// funding tx is broadcast, real fees spent), but SettleInvoice fails all its
// retries. The order must NOT be marked COMPLETED -- the client's HTLC was
// never actually captured -- and must NOT be cancelled either, since the
// channel is already open and irreversible at that point. Funding must
// still be recorded, so a later recovery pass can find and retry it (H-3/H-4).
func TestOpenChannelForHeldOrder_SettleInvoiceFailsPermanently_DoesNotMarkCompleted(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{
		FeeRate:       0.000001,
		BaseFeeMloki:  1000,
		TimeLockDelta: 4000,
	}

	preimageHex, preimageBytes, _ := testHeldOrderPreimage()
	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	order := &core.Lsps1Order{
		OrderId:           "held-order-settle-fails",
		PeerId:            peerPubkey,
		OrderState:        "PAID",
		PaymentState:      "HOLD",
		PaymentPreimage:   &preimageHex,
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
	}

	mockClient.On("IsConnected", peerIdBytes).Return(true, nil)
	feeEst := &chain.FeeEstimation{LokiPerVByte: func() *float64 { f := 1.0; return &f }()}
	feeEstimator.On("EstimateFeeRate", mock.Anything, chain.FeeStrategyEconomy).Return(feeEst, nil)

	chanPoint := &wire.OutPoint{Hash: chainhash.Hash{0xee}}
	mockClient.On("OpenChannel", mock.Anything).Return(chanPoint, nil)
	// SettleInvoice fails every one of settleHeldOrderInvoice's 5 attempts.
	mockClient.On("SettleInvoice", mock.Anything, preimageBytes).Return(assert.AnError)

	mockStore.On("UpdateLsps1OrderFunding", "held-order-settle-fails", chanPoint.Hash.String(), 0).Return(true, nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	svc.openChannelForHeldOrder(order)

	time.Sleep(50 * time.Millisecond)

	mockClient.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockStore.AssertNotCalled(t, "UpdateLsps1OrderState", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mockClient.AssertNotCalled(t, "CancelInvoice", mock.Anything, mock.Anything)
	mockNotification.AssertNotCalled(t, "NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestOpenChannelForHeldOrder_FundingClaimFails_DoesNotSettleOrComplete is
// the regression test for S-6: OpenChannel succeeds (a real channel is
// broadcast), but the funding claim itself doesn't succeed (a transient DB
// error, or -- per S-5 -- another order already claimed this exact channel
// point). recordHeldOrderFundingAndSettle must not proceed to
// settle/complete in that case: SettleInvoice must never be called, and
// the order must be left as-is (order_state=PAID/payment_state=HOLD, no
// FundingTxid recorded) for a later recoverStuckOrders pass to retry via
// the existing orphaned-channel recovery path.
func TestOpenChannelForHeldOrder_FundingClaimFails_DoesNotSettleOrComplete(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{
		FeeRate:       0.000001,
		BaseFeeMloki:  1000,
		TimeLockDelta: 4000,
	}

	preimageHex, _, _ := testHeldOrderPreimage()
	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	order := &core.Lsps1Order{
		OrderId:           "held-order-funding-claim-fails",
		PeerId:            peerPubkey,
		OrderState:        "PAID",
		PaymentState:      "HOLD",
		PaymentPreimage:   &preimageHex,
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
	}

	mockClient.On("IsConnected", peerIdBytes).Return(true, nil)
	feeEst := &chain.FeeEstimation{LokiPerVByte: func() *float64 { f := 1.0; return &f }()}
	feeEstimator.On("EstimateFeeRate", mock.Anything, chain.FeeStrategyEconomy).Return(feeEst, nil)

	chanPoint := &wire.OutPoint{Hash: chainhash.Hash{0xff}}
	mockClient.On("OpenChannel", mock.Anything).Return(chanPoint, nil)
	// Someone else already claimed this exact channel point (S-5).
	mockStore.On("UpdateLsps1OrderFunding", "held-order-funding-claim-fails", chanPoint.Hash.String(), 0).Return(false, nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	svc.openChannelForHeldOrder(order)

	time.Sleep(50 * time.Millisecond)

	mockClient.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockClient.AssertNotCalled(t, "SettleInvoice", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "UpdateLsps1OrderState", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mockNotification.AssertNotCalled(t, "NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestOpenChannelForOrder_FundingClaimFails_DoesNotMarkCompleted is
// S-6's legacy-flow counterpart: OpenChannel succeeds, but
// UpdateLsps1OrderFunding fails outright (a transient DB error). The order
// must not be marked COMPLETED with no funding_txid ever persisted --
// UpdateLsps1OrderState must never be called.
func TestOpenChannelForOrder_FundingClaimFails_DoesNotMarkCompleted(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{
		FeeRate:       0.000001,
		BaseFeeMloki:  1000,
		TimeLockDelta: 4000,
	}

	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	order := &core.Lsps1Order{
		OrderId:           "legacy-order-funding-write-fails",
		PeerId:            peerPubkey,
		OrderState:        "PAID",
		PaymentState:      "PAID",
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
	}

	mockClient.On("IsConnected", peerIdBytes).Return(true, nil)
	feeEst := &chain.FeeEstimation{LokiPerVByte: func() *float64 { f := 1.0; return &f }()}
	feeEstimator.On("EstimateFeeRate", mock.Anything, chain.FeeStrategyEconomy).Return(feeEst, nil)

	chanPoint := &wire.OutPoint{Hash: chainhash.Hash{0x11}}
	mockClient.On("OpenChannel", mock.Anything).Return(chanPoint, nil)
	mockStore.On("UpdateLsps1OrderFunding", "legacy-order-funding-write-fails", chanPoint.Hash.String(), 0).Return(false, assert.AnError)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	svc.openChannelForOrder(order)

	time.Sleep(50 * time.Millisecond)

	mockClient.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockStore.AssertNotCalled(t, "UpdateLsps1OrderState", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mockNotification.AssertNotCalled(t, "NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestRecoverStuckOrders_HeldOrderWithFunding_RoutesToHoldAwareRecovery is
// the regression test for H-3/H-4: a HOLD order that crashed AFTER funding
// was recorded (the channel-open half of an earlier openChannelForHeldOrder
// attempt succeeded) must still go through the hold-aware recovery path
// (which verifies the channel and retries settlement) rather than the
// settlement-unaware reconcileFundedOrder, which would mark it COMPLETED
// purely because a channel exists -- without ever checking or retrying
// SettleInvoice. If routing were wrong (FundingTxid checked before
// PaymentState, the pre-fix behavior), GetChannel/SettleInvoice below would
// never be called and UpdateLsps1OrderState would fire with the legacy
// (non-preimage-aware) COMPLETED write instead.
func TestRecoverStuckOrders_HeldOrderWithFunding_RoutesToHoldAwareRecovery(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{}

	preimageHex, preimageBytes, _ := testHeldOrderPreimage()
	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	txid := "aabbccdd"
	outpoint := 0
	heldOrder := &core.Lsps1Order{
		OrderId:         "held-recovery-funded",
		PeerId:          peerPubkey,
		OrderState:      "PAID",
		PaymentState:    "HOLD",
		PaymentPreimage: &preimageHex,
		FundingTxid:     &txid,
		FundingOutpoint: &outpoint,
	}

	mockStore.On("GetAllPaidLsps1Orders").Return([]*core.Lsps1Order{heldOrder}, nil)
	mockStore.On("GetAllCreatedLsps1Orders").Return([]*core.Lsps1Order{}, nil)

	channelPoint, err := lightning.NewOutPointFromString(fmt.Sprintf("%s:%d", txid, outpoint))
	require.NoError(t, err)
	mockClient.On("GetChannel", peerIdBytes, *channelPoint).Return(&lightning.GetChannelResult{}, nil)
	mockClient.On("SettleInvoice", mock.Anything, preimageBytes).Return(nil)
	mockStore.On("UpdateLsps1OrderState", "held-recovery-funded", "COMPLETED", "PAID", "PAID").Return(true, nil)
	mockNotification.On("NotifyOrderStateChanged", peerPubkey, "held-recovery-funded", "COMPLETED", fmt.Sprintf("%s:%d", txid, outpoint), "").Return(true, nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	svc.recoverStuckOrders()

	time.Sleep(50 * time.Millisecond)

	mockClient.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockNotification.AssertExpectations(t)
	mockClient.AssertNotCalled(t, "OpenChannel", mock.Anything)
}

// TestReconcileFundedHeldOrder_ChannelNotActive_DoesNotSettle proves the
// channel-verification gate: if the recorded funding outpoint doesn't
// correspond to an active channel in LND (e.g. force-closed before recovery
// ran), settlement must not be attempted and the order must be left as-is
// for manual review -- mirroring reconcileFundedOrder's own conservatism
// for the legacy flow.
func TestReconcileFundedHeldOrder_ChannelNotActive_DoesNotSettle(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{}

	preimageHex, _, _ := testHeldOrderPreimage()
	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	txid := "aabbccdd"
	outpoint := 0
	order := &core.Lsps1Order{
		OrderId:         "held-recovery-force-closed",
		PeerId:          peerPubkey,
		OrderState:      "PAID",
		PaymentState:    "HOLD",
		PaymentPreimage: &preimageHex,
		FundingTxid:     &txid,
		FundingOutpoint: &outpoint,
	}

	channelPoint, err := lightning.NewOutPointFromString(fmt.Sprintf("%s:%d", txid, outpoint))
	require.NoError(t, err)
	mockClient.On("GetChannel", peerIdBytes, *channelPoint).Return(nil, assert.AnError)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	svc.reconcileFundedHeldOrder(order)

	mockClient.AssertExpectations(t)
	mockClient.AssertNotCalled(t, "SettleInvoice", mock.Anything, mock.Anything)
	mockStore.AssertNotCalled(t, "UpdateLsps1OrderState", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestVerifyChannelActive_NilFunding_ReturnsFalseNoPanic is the regression
// test for S-8: verifyChannelActive is documented as a shared, reusable
// helper, but previously dereferenced order.FundingTxid/FundingOutpoint
// with no nil-check of its own -- both current callers happen to always
// guard it first, so this never fires in practice, but a future caller
// (or a reordering of an existing one, the exact class of mistake H-4 was)
// would panic instead of failing loudly.
func TestVerifyChannelActive_NilFunding_ReturnsFalseNoPanic(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	conf := &config.NodeConfig{}

	order := &core.Lsps1Order{
		OrderId: "order-no-funding-recorded",
		PeerId:  "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f",
		// FundingTxid/FundingOutpoint deliberately left nil.
	}

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, nil)

	assert.NotPanics(t, func() {
		result := svc.verifyChannelActive(order)
		assert.False(t, result)
	})
	mockClient.AssertNotCalled(t, "GetChannel", mock.Anything, mock.Anything)
}

// TestRecoverSettledCreatedOrders_AcceptedInvoice_RoutesToHeldOrderPath
// covers the other startup-recovery gap: an order that crashed while still
// CREATED (before the live ACCEPTED event was ever processed), where LND
// shows the invoice already ACCEPTED by the time flspd restarts. This must
// go through processAcceptedOrder (settle-or-cancel), not be silently
// ignored the way InvoiceStateOpen is.
func TestRecoverSettledCreatedOrders_AcceptedInvoice_RoutesToHeldOrderPath(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{}

	preimageHex, _, hash := testHeldOrderPreimage()
	invoice := "lnbc1holdrecoverytest..."
	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	createdOrder := &core.Lsps1Order{
		OrderId:           "held-recovery-2",
		PeerId:            peerPubkey,
		OrderState:        "CREATED",
		PaymentState:      "EXPECT_PAYMENT",
		PaymentInvoice:    invoice,
		PaymentPreimage:   &preimageHex,
		ClientBalanceLoki: 1000000,
	}

	mockStore.On("GetAllPaidLsps1Orders").Return([]*core.Lsps1Order{}, nil)
	mockStore.On("GetAllCreatedLsps1Orders").Return([]*core.Lsps1Order{createdOrder}, nil)
	// H-11: a hold-invoice order (has PaymentPreimage) looks up its state
	// by hash, derived locally, instead of LookupInvoiceState's
	// DecodePayReq round-trip.
	mockClient.On("LookupInvoiceStateByHash", mock.Anything, hash[:]).Return(lightning.InvoiceStateAccepted, nil)

	mockStore.On("UpdateLsps1OrderState", "held-recovery-2", "PAID", "HOLD", "CREATED").Return(true, nil)
	mockNotification.On("NotifyOrderStateChanged", peerPubkey, "held-recovery-2", "PAID", "", "").Return(true, nil)

	// Use the disconnected-peer shortcut (already proven above) to reach a
	// terminal outcome with minimal extra mocking -- the point here is
	// proving recoverSettledCreatedOrders reaches openChannelForHeldOrder
	// at all, not re-testing its internals.
	mockClient.On("IsConnected", peerIdBytes).Return(false, nil)
	mockClient.On("CancelInvoice", mock.Anything, hash[:]).Return(nil)
	mockStore.On("UpdateLsps1OrderState", "held-recovery-2", "FAILED", "REFUNDED", "PAID").Return(true, nil)
	mockNotification.On("NotifyOrderStateChanged", peerPubkey, "held-recovery-2", "FAILED", "", mock.Anything).Return(true, nil)

	invoiceChan := make(chan *lightning.InvoiceUpdate, 1)
	errChan := make(chan error, 1)
	mockClient.On("SubscribeInvoices").Return((<-chan *lightning.InvoiceUpdate)(invoiceChan), (<-chan error)(errChan), nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := svc.Start(ctx)
	assert.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	svc.Stop()

	mockClient.AssertCalled(t, "CancelInvoice", mock.Anything, hash[:])
	mockStore.AssertCalled(t, "UpdateLsps1OrderState", "held-recovery-2", "FAILED", "REFUNDED", "PAID")
	mockNotification.AssertExpectations(t)
}

// TestOpenChannelForOrder_OpenChannelFails_ConcurrentWriterWins_NoSpuriousNotification
// is the regression test for a review finding: openChannelForOrder (touched
// by this diff for the H-13 buildOpenChannelRequest extraction) used to
// discard the CAS `updated` bool on its FAILED-path UpdateLsps1OrderState
// call -- the same pattern fixed in cancelHeldOrder/reconcileFundedOrder/
// reconcileUnclaimedChannel for H-5, but missed in this function. If a
// concurrent writer already moved the order out of PAID (e.g. it actually
// completed via a racing path) by the time this FAILED write runs, the CAS
// silently no-ops, but the old code still fired a FAILED notification for
// an order the DB shows as something else entirely.
func TestOpenChannelForOrder_OpenChannelFails_ConcurrentWriterWins_NoSpuriousNotification(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{
		FeeRate:       0.000001,
		BaseFeeMloki:  1000,
		TimeLockDelta: 4000,
	}

	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	order := &core.Lsps1Order{
		OrderId:           "concurrent-failed-order",
		PeerId:            peerPubkey,
		OrderState:        "PAID",
		PaymentState:      "PAID",
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
	}

	mockClient.On("IsConnected", peerIdBytes).Return(true, nil)
	feeEst := &chain.FeeEstimation{LokiPerVByte: func() *float64 { f := 1.0; return &f }()}
	feeEstimator.On("EstimateFeeRate", mock.Anything, chain.FeeStrategyEconomy).Return(feeEst, nil)

	mockClient.On("OpenChannel", mock.Anything).Return(nil, assert.AnError)
	// reconcileUnclaimedChannel: no existing channel to this peer, so
	// there's nothing to reconcile -- falls through to the FAILED write.
	mockClient.On("ListChannels").Return([]*lightning.Channel{}, nil)

	// Simulate a concurrent writer having already moved the order out of
	// PAID (e.g. a racing recovery path completed it): the CAS no-ops.
	mockStore.On("UpdateLsps1OrderState", "concurrent-failed-order", "FAILED", "PAID", "PAID").Return(false, nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	svc.openChannelForOrder(order)

	time.Sleep(50 * time.Millisecond)

	mockClient.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockNotification.AssertNotCalled(t, "NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// TestOpenChannelForOrder_Success_ConcurrentWriterWins_NoSpuriousNotification
// is the COMPLETED-path counterpart: OpenChannel succeeds, but the
// COMPLETED CAS no-ops because a concurrent writer already transitioned
// the order. Must not fire a COMPLETED notification for a transition that
// didn't actually happen at the DB level.
func TestOpenChannelForOrder_Success_ConcurrentWriterWins_NoSpuriousNotification(t *testing.T) {
	mockClient := new(MockLightningClient)
	mockStore := new(MockOpeningStore)
	historyStore := new(MockHistoryStore)
	feeEstimator := new(MockFeeEstimator)
	mockNotification := new(MockNotificationService)
	conf := &config.NodeConfig{
		FeeRate:       0.000001,
		BaseFeeMloki:  1000,
		TimeLockDelta: 4000,
	}

	peerPubkey := "02677063d89856f4d04be859e9a4d3393165b6f3cd78641bc3c4ef9ca2fde37f"
	peerIdBytes, _ := hex.DecodeString(peerPubkey)

	order := &core.Lsps1Order{
		OrderId:           "concurrent-completed-order",
		PeerId:            peerPubkey,
		OrderState:        "PAID",
		PaymentState:      "PAID",
		ClientBalanceLoki: 1000000,
		LspBalanceLoki:    5000000,
	}

	mockClient.On("IsConnected", peerIdBytes).Return(true, nil)
	feeEst := &chain.FeeEstimation{LokiPerVByte: func() *float64 { f := 1.0; return &f }()}
	feeEstimator.On("EstimateFeeRate", mock.Anything, chain.FeeStrategyEconomy).Return(feeEst, nil)

	chanPoint := &wire.OutPoint{Hash: chainhash.Hash{0xab}}
	mockClient.On("OpenChannel", mock.Anything).Return(chanPoint, nil)
	mockStore.On("UpdateLsps1OrderFunding", "concurrent-completed-order", chanPoint.Hash.String(), 0).Return(true, nil)
	// Simulate a concurrent writer having already moved the order out of
	// PAID: the COMPLETED CAS no-ops.
	mockStore.On("UpdateLsps1OrderState", "concurrent-completed-order", "COMPLETED", "PAID", "PAID").Return(false, nil)

	svc := NewLsps1SettlementService(mockClient, mockStore, historyStore, config.NewNodeConfigRef(conf), feeEstimator, chain.FeeStrategyEconomy, mockNotification)

	svc.openChannelForOrder(order)

	time.Sleep(50 * time.Millisecond)

	mockClient.AssertExpectations(t)
	mockStore.AssertExpectations(t)
	mockNotification.AssertNotCalled(t, "NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}
