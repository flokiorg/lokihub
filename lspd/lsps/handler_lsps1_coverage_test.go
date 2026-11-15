package lsps

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flokiorg/flnd/lnrpc/invoicesrpc"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"

	// "github.com/flokiorg/lokihub/lspd/lsps/mocks" // Removed
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestHandleLsps1CreateOrder_Matrix(t *testing.T) {
	// Setup constants from LSPS1 defaults/limits for testing
	const (
		MinClient      = uint64(0)
		MaxClient      = uint64(100_000_000)
		MaxLsp         = uint64(100_000_000)
		MinChan        = uint64(20000) // Assumed default from nodesService mock
		ValidLsp       = uint64(50000)
		ValidClient    = uint64(0)
		OverflowLsp    = MaxLsp + 1
		OverflowClient = MaxClient + 1
	)

	tests := []struct {
		name          string
		clientBalance string
		lspBalance    string
		mockSetup     func(*MockLiquidityManager, *MockNodesService, *MockOpeningService, *MockLndClient, *MockNotificationService)
		expectCode    int
		expectMsg     string
	}{
		{
			name:          "Reject: Both Balances Zero",
			clientBalance: "0",
			lspBalance:    "0",
			expectCode:    -32602,
			expectMsg:     "Invalid params: either client_balance or lsp_balance must be non-zero",
		},
		{
			name:          "Reject: Client Balance Overflow",
			clientBalance: "100000001", // MaxClient + 1
			lspBalance:    "0",
			expectCode:    100,
			expectMsg:     "Client balance out of bounds",
		},
		{
			name:          "Reject: LSP Balance Overflow",
			clientBalance: "0",
			lspBalance:    "100000001", // MaxLsp + 1
			expectCode:    100,
			expectMsg:     "LSP balance out of bounds",
		},
		{
			name:          "Reject: Channel Capacity Too Small",
			clientBalance: "0",
			lspBalance:    "10000", // < MinChan (20000)
			mockSetup: func(lm *MockLiquidityManager, ns *MockNodesService, os *MockOpeningService, lc *MockLndClient, notif *MockNotificationService) {
				mockSufficientLiquidity(lm)
				ns.On("GetMinChannelCapacity").Return(MinChan)
				os.On("CountLsps1Orders", mock.Anything, mock.Anything, mock.Anything).Return(0, nil)
			},
			expectCode: 100,
			expectMsg:  "Channel capacity too small",
		},
		{
			name:          "Success: Valid Inbound Order",
			clientBalance: "0",
			lspBalance:    "50000",
			mockSetup: func(lm *MockLiquidityManager, ns *MockNodesService, os *MockOpeningService, lc *MockLndClient, notif *MockNotificationService) {
				mockSufficientLiquidity(lm)
				ns.On("GetMinChannelCapacity").Return(MinChan)
				ns.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{})}})
				os.On("GetFeeParamsMenu", mock.Anything, mock.Anything).Return([]*core.OpeningFeeParams{{
					MinFeeMloki: 1000, Proportional: 100,
				}}, nil)
				os.On("CountLsps1Orders", mock.Anything, mock.Anything, mock.Anything).Return(0, nil)
				lc.On("AddHoldInvoice", mock.Anything, mock.Anything).Return(&invoicesrpc.AddHoldInvoiceResp{PaymentRequest: "lnbc..."}, nil)
				lm.On("ReserveLsps1Order", mock.Anything, mock.Anything).Return(true, nil)
				notif.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, "CREATED", "", "").Return(true, nil)
			},
			expectCode: 0, // Success (no error field in response, but test checks result != nil)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock dependencies
			mockLM := new(MockLiquidityManager)
			mockNS := new(MockNodesService)
			mockOS := new(MockOpeningService)
			mockLC := new(MockLndClient)
			mockNotif := new(MockNotificationService)

			// Default mocks if not customized
			minClient := MinClient
			maxClient := MaxClient
			minLsp := uint64(0)
			maxLsp := MaxLsp
			minChan := MinChan
			maxChan := uint64(100_000_000)
			minReqConf := uint16(0)
			minFundingConf := uint16(1)
			supportsZeroReserve := true
			maxExpiry := uint32(201600)
			orderExpiry := uint32(86400)

			mockNS.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
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
			})}}).Maybe()

			if tt.mockSetup != nil {
				tt.mockSetup(mockLM, mockNS, mockOS, mockLC, mockNotif)
			} else {
				// Default permissive mocks for validations that proceed far enough to need them
				mockSufficientLiquidity(mockLM)
				mockNS.On("GetMinChannelCapacity").Return(MinChan).Maybe()
			}

			// Server setup
			cfg := Config{Lsps1MaxPendingOrders: 10}
			server := NewHandlers(cfg, mockLC, mockNS, mockOS, mockLM, mockNotif, nil)

			// Request construction
			reqData := map[string]interface{}{
				"client_balance_loki":            tt.clientBalance,
				"lsp_balance_loki":               tt.lspBalance,
				"required_channel_confirmations": 0,
				"funding_confirms_within_blocks": 1,
				"channel_expiry_blocks":          144,
				"token":                          "",
				"refund_onchain_address":         "",
				"announce_channel":               false,
			}
			paramsBytes, _ := json.Marshal(reqData)
			req := &JsonRpcRequest{
				Id:      1,
				Method:  "lsps1.create_order",
				Params:  paramsBytes,
				JsonRpc: "2.0",
			}
			peer := []byte("peer_id")

			// Execution
			resp := server.handleLsps1CreateOrder(req, peer)

			// Verification
			if tt.expectCode != 0 {
				assert.NotNil(t, resp.Error)
				assert.Equal(t, tt.expectCode, resp.Error.Code)
				assert.Contains(t, resp.Error.Message, tt.expectMsg)
			} else {
				assert.Nil(t, resp.Error)
				assert.NotNil(t, resp.Result)
			}
		})
	}
}

// TestHandleLsps1CreateOrder_FeeMatchesUnifiedFeeFunction is the
// regression test for F-5: handleLsps1CreateOrder used to compute its fee
// with its own hand-rolled floor(max(proportional, min)) logic instead of
// ComputeOpeningFee (the function LSPS2's handler already used), which
// rounds differently (ceiling, with overflow checks) for the same
// logical "opening fee" concept. Picks a proportional/capacity
// combination with a non-exact remainder specifically so the two
// rounding rules would disagree if LSPS1 weren't actually calling the
// shared function.
func TestHandleLsps1CreateOrder_FeeMatchesUnifiedFeeFunction(t *testing.T) {
	const (
		// Chosen (and >= the 20,000 loki network floor) so
		// capacity*proportional = 2,999,500 sits just past a 1000-mloki
		// (1 loki) boundary: the old floor-in-loki-space formula rounds
		// down to 2 loki, but ComputeOpeningFee's ceiling-in-mloki-space
		// rounds up to 3000 mloki = 3 loki before the mloki->loki
		// conversion. Not every capacity/proportional pair exercises
		// this — most land on the same integer loki value either way —
		// this one specifically doesn't.
		lspBalanceLoki = uint64(29_995)
		minFeeMloki    = uint64(100)
		proportional   = uint32(100)
	)

	wantFeeMloki, err := ComputeOpeningFee(lspBalanceLoki*1000, minFeeMloki, uint64(proportional))
	require.NoError(t, err)
	wantFeeLoki := int64(wantFeeMloki / 1000)

	// Sanity: confirm this fixture actually exercises the ceiling-vs-floor
	// difference, i.e. the old hand-rolled floor formula would have
	// produced a different (smaller-or-equal) result — otherwise this
	// test wouldn't actually catch a regression back to the old logic.
	oldFloorFeeLoki := int64((lspBalanceLoki * uint64(proportional)) / 1_000_000)
	if oldFloorFeeLoki > int64(minFeeMloki/1000) {
		require.NotEqual(t, oldFloorFeeLoki, wantFeeLoki, "fixture must exercise a case where ceiling (new) and floor (old) rounding actually disagree")
	}

	mockLM := new(MockLiquidityManager)
	mockNS := new(MockNodesService)
	mockOS := new(MockOpeningService)
	mockLC := new(MockLndClient)
	mockNotif := new(MockNotificationService)

	minClient, maxClient := uint64(0), uint64(100_000_000)
	minLsp, maxLsp := uint64(0), uint64(100_000_000)
	minChan, maxChan := uint64(20_000), uint64(100_000_000)
	minReqConf, minFundingConf := uint16(0), uint16(1)
	supportsZeroReserve := true
	maxExpiry, orderExpiry := uint32(201_600), uint32(86_400)

	mockNS.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
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
	})}})
	mockNS.On("GetMinChannelCapacity").Return(minChan)
	mockSufficientLiquidity(mockLM)
	mockOS.On("GetFeeParamsMenu", mock.Anything, mock.Anything).Return([]*core.OpeningFeeParams{{
		MinFeeMloki: minFeeMloki, Proportional: proportional,
	}}, nil)
	mockOS.On("CountLsps1Orders", mock.Anything, mock.Anything, mock.Anything).Return(0, nil)
	mockLC.On("AddHoldInvoice", mock.Anything, mock.MatchedBy(func(req *invoicesrpc.AddHoldInvoiceRequest) bool {
		return req.Value == wantFeeLoki // clientBalance is 0, so order total == fee
	})).Return(&invoicesrpc.AddHoldInvoiceResp{PaymentRequest: "lnbc..."}, nil)
	mockLM.On("ReserveLsps1Order", mock.Anything, mock.MatchedBy(func(order *core.Lsps1Order) bool {
		return order.FeeTotalLoki == wantFeeLoki
	})).Return(true, nil)
	mockNotif.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, "CREATED", "", "").Return(true, nil)

	server := NewHandlers(Config{Lsps1MaxPendingOrders: 10}, mockLC, mockNS, mockOS, mockLM, mockNotif, nil)

	reqData := map[string]interface{}{
		"client_balance_loki":            "0",
		"lsp_balance_loki":               fmt.Sprintf("%d", lspBalanceLoki),
		"required_channel_confirmations": 0,
		"funding_confirms_within_blocks": 1,
		"channel_expiry_blocks":          144,
		"token":                          "",
		"refund_onchain_address":         "",
		"announce_channel":               false,
	}
	paramsBytes, _ := json.Marshal(reqData)
	req := &JsonRpcRequest{Id: 1, Method: "lsps1.create_order", Params: paramsBytes, JsonRpc: "2.0"}

	resp := server.handleLsps1CreateOrder(req, []byte("peer_id"))

	require.Nil(t, resp.Error)
	mockOS.AssertExpectations(t)
	mockLC.AssertExpectations(t)
}
