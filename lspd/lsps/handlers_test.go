package lsps

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/invoicesrpc"
	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Mocks
type MockLndClient struct {
	mock.Mock
}

func (m *MockLndClient) SubscribeCustomMessages(ctx context.Context, req *lnrpc.SubscribeCustomMessagesRequest) (lnrpc.Lightning_SubscribeCustomMessagesClient, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(lnrpc.Lightning_SubscribeCustomMessagesClient), args.Error(1)
}

func (m *MockLndClient) SendCustomMessage(ctx context.Context, msg *lnrpc.SendCustomMessageRequest) (*lnrpc.SendCustomMessageResponse, error) {
	args := m.Called(ctx, msg)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*lnrpc.SendCustomMessageResponse), args.Error(1)
}

func (m *MockLndClient) AddInvoice(ctx context.Context, req *lnrpc.Invoice) (*lnrpc.AddInvoiceResponse, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*lnrpc.AddInvoiceResponse), args.Error(1)
}

func (m *MockLndClient) AddHoldInvoice(ctx context.Context, req *invoicesrpc.AddHoldInvoiceRequest) (*invoicesrpc.AddHoldInvoiceResp, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*invoicesrpc.AddHoldInvoiceResp), args.Error(1)
}

func (m *MockLndClient) GetBlockHeight() (uint32, error) {
	args := m.Called()
	return args.Get(0).(uint32), args.Error(1)
}

func (m *MockLndClient) CancelInvoice(ctx context.Context, paymentHash []byte) error {
	args := m.Called(ctx, paymentHash)
	return args.Error(0)
}

type MockNodesService struct {
	mock.Mock
}

func (m *MockNodesService) GetNode(token string) (*core.Node, error) {
	args := m.Called(token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.Node), args.Error(1)
}

func (m *MockNodesService) GetNodes() []*core.Node {
	args := m.Called()
	return args.Get(0).([]*core.Node)
}

func (m *MockNodesService) GetMinChannelCapacity() uint64 {
	args := m.Called()
	return args.Get(0).(uint64)
}

type MockOpeningService struct {
	mock.Mock
}

func (m *MockOpeningService) GetFeeParamsMenu(token string, privateKey *btcec.PrivateKey) ([]*core.OpeningFeeParams, error) {
	args := m.Called(token, privateKey)
	return args.Get(0).([]*core.OpeningFeeParams), args.Error(1)
}

func (m *MockOpeningService) ValidateOpeningFeeParams(params *core.OpeningFeeParams, publicKey *btcec.PublicKey) bool {
	args := m.Called(params, publicKey)
	return args.Bool(0)
}

func (m *MockOpeningService) IsCurrentChainFeeCheaper(currentFeeratePerVByte *float64, referenceFeeratePerVByte float64) bool {
	args := m.Called(currentFeeratePerVByte, referenceFeeratePerVByte)
	return args.Bool(0)
}

func (m *MockOpeningService) GetLsps1Order(orderId string) (*core.Lsps1Order, error) {
	args := m.Called(orderId)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningService) RegisterLsps5Webhook(webhook *core.Lsps5Webhook) error {
	args := m.Called(webhook)
	return args.Error(0)
}

func (m *MockOpeningService) GetLsps5Webhooks(clientNodeId string) ([]*core.Lsps5Webhook, error) {
	args := m.Called(clientNodeId)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*core.Lsps5Webhook), args.Error(1)
}

func (m *MockOpeningService) RemoveLsps5Webhook(clientNodeId, appName string) error {
	args := m.Called(clientNodeId, appName)
	return args.Error(0)
}

func (m *MockOpeningService) CountLsps1Orders(clientNodeId string, state string, excludeExpiredBefore *time.Time) (int, error) {
	args := m.Called(clientNodeId, state, excludeExpiredBefore)
	return args.Int(0), args.Error(1)
}

func (m *MockOpeningService) CountLsps2PendingRegistrations(peerId string) (int, error) {
	args := m.Called(peerId)
	return args.Int(0), args.Error(1)
}

func (m *MockOpeningService) CountLsps5Webhooks(clientNodeId string) (int, error) {
	args := m.Called(clientNodeId)
	return args.Int(0), args.Error(1)
}

// MockLiquidityManager
type MockLiquidityManager struct {
	mock.Mock
}

func (m *MockLiquidityManager) GetAvailableOnChainLiquidity(ctx context.Context) (uint64, error) {
	args := m.Called(ctx)
	return args.Get(0).(uint64), args.Error(1)
}

func (m *MockLiquidityManager) GetReservedOnChainLiquidity(ctx context.Context) (uint64, error) {
	args := m.Called(ctx)
	return args.Get(0).(uint64), args.Error(1)
}

func (m *MockLiquidityManager) GetLiquiditySnapshot(ctx context.Context) (LiquiditySnapshot, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return LiquiditySnapshot{}, args.Error(1)
	}
	return args.Get(0).(LiquiditySnapshot), args.Error(1)
}

// sufficientLiquiditySnapshot is a LiquiditySnapshot whose AvailableLoki()
// comfortably exceeds anything a test might request -- the
// GetLiquiditySnapshot-mock equivalent of the old
// CheckOnChainLiquidity(mock.Anything, mock.Anything).Return(nil) pattern
// used throughout this package's tests before checkFleetCeilingAndLiquidity
// unified the two checks into one fetch.
var sufficientLiquiditySnapshot = LiquiditySnapshot{ConfirmedBalanceLoki: 1_000_000_000_000}

// mockSufficientLiquidity registers a GetLiquiditySnapshot expectation
// that always reports comfortably-available liquidity, for tests that
// only care about reaching/passing the liquidity check, not its exact
// threshold.
func mockSufficientLiquidity(m *MockLiquidityManager) {
	m.On("GetLiquiditySnapshot", mock.Anything).Return(sufficientLiquiditySnapshot, nil)
}

func (m *MockLiquidityManager) ReserveLsps1Order(ctx context.Context, order *core.Lsps1Order) (bool, error) {
	args := m.Called(ctx, order)
	return args.Bool(0), args.Error(1)
}

func (m *MockLiquidityManager) ReserveLsps2Buy(ctx context.Context, reg *core.Lsps2BuyRegistration) (bool, error) {
	args := m.Called(ctx, reg)
	return args.Bool(0), args.Error(1)
}

func (m *MockLiquidityManager) CheckOnChainLiquidity(ctx context.Context, amountSat uint64) error {
	args := m.Called(ctx, amountSat)
	return args.Error(0)
}

// MockNotificationService
type MockNotificationService struct {
	mock.Mock
}

func (m *MockNotificationService) NotifyWebhookRegistered(pubkey string, webhook *core.Lsps5Webhook) bool {
	args := m.Called(pubkey, webhook)
	return args.Bool(0)
}

func (m *MockNotificationService) NotifyOrderStateChanged(pubkey string, orderId string, state string, channelPoint string, errorMsg string) (bool, error) {
	args := m.Called(pubkey, orderId, state, channelPoint, errorMsg)
	return args.Bool(0), args.Error(1)
}

// Tests
func TestLsps2Buy(t *testing.T) {
	// Setup
	mockLnd := new(MockLndClient)
	mockNodes := new(MockNodesService)
	mockOpening := new(MockOpeningService)
	mockLiquidity := new(MockLiquidityManager)
	mockNotification := new(MockNotificationService)

	server := NewHandlers(Config{Lsps1MaxPendingOrders: 5, Lsps5MaxWebhooks: 10}, mockLnd, mockNodes, mockOpening, mockLiquidity, mockNotification, nil)

	// Test Data
	privKey, pubKey := btcec.PrivKeyFromBytes([]byte("testprivkey1111111111111111111"))
	nodeId, _ := hex.DecodeString("033333333333333333333333333333333333333333333333333333333333333333")
	node := &core.Node{
		NodeId:     nodeId,
		PublicKey:  pubKey,
		PrivateKey: privKey,
		Tokens:     []string{"test-token"},
		NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
			TimeLockDelta: 144,
			JitEnabled:    true, // LSPS2 is served only while JIT is on
		}),
	}

	mockNodes.On("GetNodes").Return([]*core.Node{node})
	mockNodes.On("GetMinChannelCapacity").Return(uint64(20000))

	// Valid Params
	validUntil := time.Now().Add(24 * time.Hour).Format(time.RFC3339)

	mockOpening.On("ValidateOpeningFeeParams", mock.AnythingOfType("*core.OpeningFeeParams"), pubKey).Return(true)

	// Mock Liquidity Check (Success)
	mockSufficientLiquidity(mockLiquidity)
	mockLiquidity.On("ReserveLsps2Buy", mock.Anything, mock.AnythingOfType("*core.Lsps2BuyRegistration")).Return(true, nil)

	// Construct Request
	buyParams := Lsps2BuyParams{
		OpeningFeeParams: Lsps2OpeningFeeParams{
			MinFeeMloki:          "1000",
			Proportional:         500,
			ValidUntil:           validUntil,
			MinLifetime:          10080,
			MaxClientToSelfDelay: 2016,
			MinPaymentSizeMloki:  "1000",
			MaxPaymentSizeMloki:  "100000000",
			Promise:              "valid_promise",
		},
		PaymentSizeMloki: "50000000",
	}
	paramsBytes, _ := json.Marshal(buyParams)
	req := &JsonRpcRequest{
		Method: "lsps2.buy",
		Params: paramsBytes,
		Id:     "1",
	}

	// Execute via route (bypassing listener loop for unit test)
	resp := server.Route(req, []byte("peer_id"))

	// Verify
	assert.Nil(t, resp.Error)
	assert.NotNil(t, resp.Result)

	result, ok := resp.Result.(Lsps2BuyResult)
	if !ok {
		// handle map[string]interface{} if json unmarshalling happened?
		// No, route returns types directly.
		// NOTE: route returns *JsonRpcResponse struct. Result field is interface{}.
		// Inside lsps2Buy, we assign Lsps2BuyResult struct.
		assert.IsType(t, Lsps2BuyResult{}, resp.Result)
		result = resp.Result.(Lsps2BuyResult)
	}

	assert.NotEmpty(t, result.JITChannelScid)
	assert.Equal(t, uint16(144), result.LspCltvExpiryDelta)
	assert.Equal(t, hex.EncodeToString(nodeId), result.LspNodeId)

	mockOpening.AssertExpectations(t)
}

func TestLsps2Buy_PaymentSizeValidation(t *testing.T) {
	// Setup
	mockLnd := new(MockLndClient)
	mockNodes := new(MockNodesService)
	mockOpening := new(MockOpeningService)
	mockLiquidity := new(MockLiquidityManager)
	mockNotification := new(MockNotificationService)

	server := NewHandlers(Config{Lsps1MaxPendingOrders: 5, Lsps5MaxWebhooks: 10}, mockLnd, mockNodes, mockOpening, mockLiquidity, mockNotification, nil)

	// Test Data
	privKey, pubKey := btcec.PrivKeyFromBytes([]byte("testprivkey1111111111111111111"))
	node := &core.Node{
		PublicKey:  pubKey,
		PrivateKey: privKey,
		Tokens:     []string{"test-token"},
		NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
			TimeLockDelta: 144,
			JitEnabled:    true, // LSPS2 is served only while JIT is on
		}),
	}

	mockNodes.On("GetNodes").Return([]*core.Node{node})

	validUntil := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	mockOpening.On("ValidateOpeningFeeParams", mock.AnythingOfType("*core.OpeningFeeParams"), pubKey).Return(true)

	// Mock Liquidity Checks (Assuming they pass for validation tests unless specific failure tested)
	// Actually for validation failure BEFORE liquidity check, we don't need to mock it.
	// But let's mock it for the ones that reach it.
	mockNodes.On("GetMinChannelCapacity").Return(uint64(0)).Maybe()

	// Case 1: Payment Size Too Small (Fee >= Amount) -> Reaches liquidity check first?
	// Handler sequence:
	// 1. Parse params
	// 2. Bound validation (Min/Max)
	// 3. Liquidity Check <--- This comes before computation? Yes.
	// 4. Compute Fee

	// So for Case 1, we DO need to mock liquidity check because it passes minimal bounds and reaches check.
	mockSufficientLiquidity(mockLiquidity)

	// Case 1: Payment Size Too Small (Fee >= Amount)
	// MinFee = 1000, PaymentSize = 1000 -> Fee = 1000. Fee >= Amount (1000 >= 1000) -> Error
	buyParams1 := Lsps2BuyParams{
		OpeningFeeParams: Lsps2OpeningFeeParams{
			MinFeeMloki:          "1000",
			Proportional:         0,
			ValidUntil:           validUntil,
			MinLifetime:          10080,
			MaxClientToSelfDelay: 2016,
			MinPaymentSizeMloki:  "1000",
			MaxPaymentSizeMloki:  "1000000",
			Promise:              "valid_promise",
		},
		PaymentSizeMloki: "1000",
	}
	paramsBytes1, _ := json.Marshal(buyParams1)
	req1 := &JsonRpcRequest{Method: "lsps2.buy", Params: paramsBytes1, Id: "1"}
	resp1 := server.Route(req1, []byte("peer"))
	assert.NotNil(t, resp1.Error)
	assert.Equal(t, 202, resp1.Error.Code)

	// Case 2: Payment Size Out of Bounds
	// This fails BEFORE liquidity check.
	buyParams2 := Lsps2BuyParams{
		OpeningFeeParams: Lsps2OpeningFeeParams{
			MinFeeMloki:          "0",
			Proportional:         0,
			ValidUntil:           validUntil,
			MinLifetime:          10080,
			MaxClientToSelfDelay: 2016,
			MinPaymentSizeMloki:  "1000",
			MaxPaymentSizeMloki:  "1000000",
			Promise:              "valid_promise",
		},
		PaymentSizeMloki: "500", // < MinPaymentSizeMloki
	}
	paramsBytes2, _ := json.Marshal(buyParams2)
	req2 := &JsonRpcRequest{Method: "lsps2.buy", Params: paramsBytes2, Id: "2"}
	resp2 := server.Route(req2, []byte("peer"))
	assert.NotNil(t, resp2.Error)
	assert.Equal(t, 203, resp2.Error.Code)
}

func TestLsps1CreateOrder(t *testing.T) {
	// Setup
	mockLnd := new(MockLndClient)
	mockNodes := new(MockNodesService)
	mockOpening := new(MockOpeningService)
	mockLiquidity := new(MockLiquidityManager)
	mockNotification := new(MockNotificationService)

	server := NewHandlers(Config{Lsps1MaxPendingOrders: 5, Lsps5MaxWebhooks: 10}, mockLnd, mockNodes, mockOpening, mockLiquidity, mockNotification, nil)

	// Test Data
	privKey, pubKey := btcec.PrivKeyFromBytes([]byte("testprivkey1111111111111111111"))
	minClient := uint64(0)
	maxClient := uint64(100_000_000)
	minLsp := uint64(0)
	maxLsp := uint64(100_000_000)
	minReqConf := uint16(0)
	minFundingConf := uint16(1)
	supportsZeroReserve := true
	maxExpiry := uint32(201600)
	orderExpiry := uint32(86400)
	minChan := uint64(20000)
	maxChan := uint64(100_000_000)

	node := &core.Node{
		PublicKey:  pubKey,
		PrivateKey: privKey,
		Tokens:     []string{"test-token"},
		NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
			TimeLockDelta:                        144,
			Lsps1MinRequiredChannelConfirmations: &minReqConf,
			Lsps1MinFundingConfirmsWithinBlocks:  &minFundingConf,
			Lsps1SupportsZeroChannelReserve:      &supportsZeroReserve,
			Lsps1MaxChannelExpiryBlocks:          &maxExpiry,
			Lsps1OrderExpirySeconds:              &orderExpiry,
			Lsps1MinInitialClientBalanceLoki:     &minClient,
			Lsps1MaxInitialClientBalanceLoki:     &maxClient,
			Lsps1MinInitialLspBalanceLoki:        &minLsp,
			Lsps1MaxInitialLspBalanceLoki:        &maxLsp,
			Lsps1MinChannelBalanceLoki:           &minChan,
			Lsps1MaxChannelBalanceLoki:           &maxChan,
		}),
	}

	mockNodes.On("GetNodes").Return([]*core.Node{node})

	// Mock Fee Params Menu
	validUntil := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	fees := []*core.OpeningFeeParams{
		{
			MinFeeMloki:          1000,
			Proportional:         1000, // 1000 ppm = 0.1%
			ValidUntil:           validUntil,
			MinLifetime:          10080,
			MaxClientToSelfDelay: 2016,
			MinPaymentSizeMloki:  1000,
			MaxPaymentSizeMloki:  100000000,
			Promise:              "promise",
		},
	}
	mockOpening.On("GetFeeParamsMenu", "test-token", node.PrivateKey).Return(fees, nil)
	mockOpening.On("CountLsps1Orders", mock.Anything, "CREATED", mock.AnythingOfType("*time.Time")).Return(0, nil)

	// Mock Min Channel Capacity
	mockNodes.On("GetMinChannelCapacity").Return(uint64(20000))

	// Mock Liquidity Check (Success)
	mockSufficientLiquidity(mockLiquidity)

	// Mock AddHoldInvoice
	mockLnd.On("AddHoldInvoice", mock.Anything, mock.AnythingOfType("*invoicesrpc.AddHoldInvoiceRequest")).Return(&invoicesrpc.AddHoldInvoiceResp{
		PaymentRequest: "lnbcrt1...",
	}, nil)

	// Mock Register Order
	mockLiquidity.On("ReserveLsps1Order", mock.Anything, mock.AnythingOfType("*core.Lsps1Order")).Return(true, nil)

	// Execute
	params := Lsps1CreateOrderRequest{
		LspBalanceLoki:               "0",
		ClientBalanceLoki:            "100000",
		RequiredChannelConfirmations: 0,
		FundingConfirmsWithinBlocks:  6,
		ChannelExpiryBlocks:          2016,
		Token:                        "test-token",
		RefundOnchainAddress:         "",
		AnnounceChannel:              false,
	}
	paramsBytes, _ := json.Marshal(params)
	req := &JsonRpcRequest{Method: "lsps1.create_order", Params: paramsBytes, Id: "1"}

	// Expect notification
	done := make(chan struct{})
	mockNotification.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(true, nil).Run(func(args mock.Arguments) {
		close(done)
	})

	resp := server.Route(req, []byte("peer"))

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for notification")
	}

	// Verify
	assert.Nil(t, resp.Error)
	assert.NotNil(t, resp.Result)

	result := resp.Result.(Lsps1CreateOrderResponse)
	assert.Equal(t, "CREATED", result.OrderState)
	assert.Equal(t, "EXPECT_PAYMENT", result.Payment.Bolt11.State)
	// Fee check: Client=100_000. Fee = Min(1) + 100_000*1000/1_000_000 = 1 + 100 = 101.
	// Wait, inputs are sat (loki). Params are mloki.
	// Fee calculation in handler:
	// capacity = 100_000
	// proportionalFee = 100_000 * 1000 / 1_000_000 = 100 loki.
	// minFeeLoki = 1000 / 1000 = 1 loki.
	// Total fee = 101.
	assert.Equal(t, "100", result.Payment.Bolt11.FeeTotalLoki)
	assert.Equal(t, "100100", result.Payment.Bolt11.OrderTotalLoki)

	mockOpening.AssertExpectations(t)
	mockLnd.AssertExpectations(t)
	mockNotification.AssertExpectations(t)
}
