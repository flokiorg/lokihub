package lsps

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc/invoicesrpc"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestHandleLsps1CreateOrder_F24_ChecksFullCapacityNotJustLspBalance is the
// regression test for F-24: order creation used to liquidity-check only
// lsp_balance_loki, even though buildOpenChannelRequest actually funds
// lsp_balance+client_balance+padding. Constructs a case where lsp_balance
// alone is well within a mocked liquidity limit, but the true required
// capacity (including a real client_balance and padding) is not -- if the
// bug were still present, CheckOnChainLiquidity would be called with just
// lspBalance (50_000) and the order would succeed; with the fix, it must
// be called with the true finalCapacity (150_000+padding) and get
// rejected once the mock is wired to only accept up to 100_000.
func TestHandleLsps1CreateOrder_F24_ChecksFullCapacityNotJustLspBalance(t *testing.T) {
	mockLM := new(MockLiquidityManager)
	mockNS := new(MockNodesService)
	mockOS := new(MockOpeningService)
	mockLC := new(MockLndClient)
	mockNotif := new(MockNotificationService)

	const (
		lspBalance    = uint64(50_000)
		clientBalance = uint64(100_000)
		padding       = int64(5_000)
	)
	minChan := uint64(20_000)
	finalCapacity := lspBalance + clientBalance + uint64(padding) // 155_000

	minReqConf := uint16(0)
	minFundingConf := uint16(1)
	supportsZeroReserve := true
	maxExpiry := uint32(201600)
	orderExpiry := uint32(86400)
	maxClient := uint64(1_000_000)
	maxLsp := uint64(1_000_000)
	maxChan := uint64(1_000_000)

	mockNS.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
		AdditionalChannelCapacity:            padding,
		Lsps1MinRequiredChannelConfirmations: &minReqConf,
		Lsps1MinFundingConfirmsWithinBlocks:  &minFundingConf,
		Lsps1SupportsZeroChannelReserve:      &supportsZeroReserve,
		Lsps1MaxChannelExpiryBlocks:          &maxExpiry,
		Lsps1OrderExpirySeconds:              &orderExpiry,
		Lsps1MinInitialClientBalanceLoki:     new(uint64),
		Lsps1MaxInitialClientBalanceLoki:     &maxClient,
		Lsps1MinInitialLspBalanceLoki:        new(uint64),
		Lsps1MaxInitialLspBalanceLoki:        &maxLsp,
		Lsps1MinChannelBalanceLoki:           &minChan,
		Lsps1MaxChannelBalanceLoki:           &maxChan,
	})}})
	mockNS.On("GetMinChannelCapacity").Return(minChan)

	// Only 100_000 loki is actually available -- the buggy pre-fix code
	// path (checking lspBalance=50_000 alone) would pass against this;
	// the fixed code path (checking finalCapacity=155_000) must not.
	mockLM.On("GetLiquiditySnapshot", mock.Anything).Return(LiquiditySnapshot{ConfirmedBalanceLoki: 100_000}, nil)
	mockOS.On("CountLsps1Orders", mock.Anything, mock.Anything, mock.Anything).Return(0, nil)

	cfg := Config{Lsps1MaxPendingOrders: 10}
	server := NewHandlers(cfg, mockLC, mockNS, mockOS, mockLM, mockNotif, nil)

	reqData := map[string]interface{}{
		"client_balance_loki":            "100000",
		"lsp_balance_loki":               "50000",
		"required_channel_confirmations": 0,
		"funding_confirms_within_blocks": 1,
		"channel_expiry_blocks":          144,
	}
	paramsBytes, _ := json.Marshal(reqData)
	req := &JsonRpcRequest{Id: 1, Method: "lsps1.create_order", Params: paramsBytes, JsonRpc: "2.0"}

	resp := server.handleLsps1CreateOrder(req, []byte("peer_id"))

	require.NotNil(t, resp.Error, "F-24: order must be rejected once the FULL committed capacity (%d) is checked, not just lsp_balance_loki (%d) against the 100_000 available", finalCapacity, lspBalance)
	assert.Contains(t, resp.Error.Message, "insufficient liquidity")
}

// TestHandleLsps1CreateOrder_F7_ReservedCapacityIncludesBolt2ReserveTopUp
// is the regression test for the second-pass finding that LSPS1's own
// reserved capacity never included the BOLT2 ~1% reserve top-up term that
// the interceptor's JIT-reconnect-fallback path (used when the peer
// wasn't connected at settlement time) DOES apply via the same shared
// config.RequiredCapacityLoki -- so an order funded through that fallback
// path could be opened larger than what was ever reserved for it. Uses a
// client-balance-dominated order (clientBalance*100/99 exceeds
// channelCapacity+padding) so the two formulas' outputs actually diverge
// if the fix isn't applied.
func TestHandleLsps1CreateOrder_F7_ReservedCapacityIncludesBolt2ReserveTopUp(t *testing.T) {
	mockLM := new(MockLiquidityManager)
	mockNS := new(MockNodesService)
	mockOS := new(MockOpeningService)
	mockLC := new(MockLndClient)
	mockNotif := new(MockNotificationService)

	const (
		lspBalance    = uint64(0)
		clientBalance = uint64(1_000_000)
		padding       = int64(1_000)
	)
	minChan := uint64(20_000)
	channelCapacity := lspBalance + clientBalance
	oldFormulaCapacity := channelCapacity + uint64(padding) // 1_001_000 -- what the pre-fix code computed
	reserveTopUp := clientBalance*100/99 + uint64(padding)  // 1_011_101 -- BOLT2 headroom term
	wantCapacity := reserveTopUp                            // the larger of the two must win

	minReqConf := uint16(0)
	minFundingConf := uint16(1)
	supportsZeroReserve := true
	maxExpiry := uint32(201600)
	orderExpirySeconds := uint32(3600)
	maxClient := uint64(10_000_000)
	maxLsp := uint64(10_000_000)
	maxChan := uint64(10_000_000)

	mockNS.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
		AdditionalChannelCapacity:            padding,
		Lsps1MinRequiredChannelConfirmations: &minReqConf,
		Lsps1MinFundingConfirmsWithinBlocks:  &minFundingConf,
		Lsps1SupportsZeroChannelReserve:      &supportsZeroReserve,
		Lsps1MaxChannelExpiryBlocks:          &maxExpiry,
		Lsps1OrderExpirySeconds:              &orderExpirySeconds,
		Lsps1MinInitialClientBalanceLoki:     new(uint64),
		Lsps1MaxInitialClientBalanceLoki:     &maxClient,
		Lsps1MinInitialLspBalanceLoki:        new(uint64),
		Lsps1MaxInitialLspBalanceLoki:        &maxLsp,
		Lsps1MinChannelBalanceLoki:           &minChan,
		Lsps1MaxChannelBalanceLoki:           &maxChan,
	})}})
	mockNS.On("GetMinChannelCapacity").Return(minChan)
	mockSufficientLiquidity(mockLM)
	mockOS.On("GetFeeParamsMenu", mock.Anything, mock.Anything).Return([]*core.OpeningFeeParams{{MinFeeMloki: 1000, Proportional: 100}}, nil)
	mockOS.On("CountLsps1Orders", mock.Anything, mock.Anything, mock.Anything).Return(0, nil)
	mockLC.On("AddHoldInvoice", mock.Anything, mock.Anything).Return(&invoicesrpc.AddHoldInvoiceResp{PaymentRequest: "lnbc..."}, nil)
	mockNotif.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, "CREATED", "", "").Return(true, nil)

	var captured *core.Lsps1Order
	mockLM.On("ReserveLsps1Order", mock.Anything, mock.MatchedBy(func(o *core.Lsps1Order) bool {
		captured = o
		return true
	})).Return(true, nil)

	cfg := Config{Lsps1MaxPendingOrders: 10}
	server := NewHandlers(cfg, mockLC, mockNS, mockOS, mockLM, mockNotif, nil)

	reqData := map[string]interface{}{
		"client_balance_loki":            "1000000",
		"lsp_balance_loki":               "0",
		"required_channel_confirmations": 0,
		"funding_confirms_within_blocks": 1,
		"channel_expiry_blocks":          144,
	}
	paramsBytes, _ := json.Marshal(reqData)
	req := &JsonRpcRequest{Id: 1, Method: "lsps1.create_order", Params: paramsBytes, JsonRpc: "2.0"}

	resp := server.handleLsps1CreateOrder(req, []byte("peer_id"))
	require.Nil(t, resp.Error)
	require.NotNil(t, captured)

	assert.Greater(t, wantCapacity, oldFormulaCapacity, "sanity check: the test fixture must actually make the two formulas diverge")
	assert.Equal(t, wantCapacity, captured.ReservedAmountLoki, "reserved capacity must include the BOLT2 reserve top-up, matching the interceptor's own RequiredCapacityLoki formula")
}

// TestHandleLsps1CreateOrder_F8_SingleConfigSnapshotPerRequest is the
// regression test for the double-config-load finding: handleLsps1CreateOrder
// used to call getLsps1ConfigValues() (which does NodeConfig.Load(), an
// atomic read of a dashboard-hot-swappable pointer) twice -- once for the
// balance-bounds check, again later for the capacity/business-floor
// check. Simulates a config update landing between what would have been
// those two loads: the FIRST GetNodes() call returns a config whose
// business ceiling comfortably allows this order's capacity; a SECOND
// call (only reachable if the bug is reintroduced) returns a config whose
// ceiling would reject that same capacity. If the fix holds (one snapshot
// used for the whole request), the order must succeed.
func TestHandleLsps1CreateOrder_F8_SingleConfigSnapshotPerRequest(t *testing.T) {
	mockLM := new(MockLiquidityManager)
	mockNS := new(MockNodesService)
	mockOS := new(MockOpeningService)
	mockLC := new(MockLndClient)
	mockNotif := new(MockNotificationService)

	minReqConf := uint16(0)
	minFundingConf := uint16(1)
	supportsZeroReserve := true
	maxExpiry := uint32(201600)
	orderExpirySeconds := uint32(3600)
	maxClient := uint64(10_000_000)
	maxLsp := uint64(10_000_000)
	minChan := uint64(20_000)

	// First snapshot (must be the ONLY one used): permits up to 200_000.
	maxChanA := uint64(200_000)
	configA := &config.NodeConfig{
		Lsps1MinRequiredChannelConfirmations: &minReqConf,
		Lsps1MinFundingConfirmsWithinBlocks:  &minFundingConf,
		Lsps1SupportsZeroChannelReserve:      &supportsZeroReserve,
		Lsps1MaxChannelExpiryBlocks:          &maxExpiry,
		Lsps1OrderExpirySeconds:              &orderExpirySeconds,
		Lsps1MinInitialClientBalanceLoki:     new(uint64),
		Lsps1MaxInitialClientBalanceLoki:     &maxClient,
		Lsps1MinInitialLspBalanceLoki:        new(uint64),
		Lsps1MaxInitialLspBalanceLoki:        &maxLsp,
		Lsps1MinChannelBalanceLoki:           &minChan,
		Lsps1MaxChannelBalanceLoki:           &maxChanA,
	}
	// Second snapshot (must NEVER be reached): a much lower ceiling that
	// would reject this exact order's capacity -- only observable if
	// getLsps1ConfigValues() is (incorrectly) called a second time.
	maxChanB := uint64(1_000)
	configB := &config.NodeConfig{
		Lsps1MinRequiredChannelConfirmations: &minReqConf,
		Lsps1MinFundingConfirmsWithinBlocks:  &minFundingConf,
		Lsps1SupportsZeroChannelReserve:      &supportsZeroReserve,
		Lsps1MaxChannelExpiryBlocks:          &maxExpiry,
		Lsps1OrderExpirySeconds:              &orderExpirySeconds,
		Lsps1MinInitialClientBalanceLoki:     new(uint64),
		Lsps1MaxInitialClientBalanceLoki:     &maxClient,
		Lsps1MinInitialLspBalanceLoki:        new(uint64),
		Lsps1MaxInitialLspBalanceLoki:        &maxLsp,
		Lsps1MinChannelBalanceLoki:           &minChan,
		Lsps1MaxChannelBalanceLoki:           &maxChanB,
	}

	mockNS.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(configA)}}).Once()
	mockNS.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(configB)}})
	mockNS.On("GetMinChannelCapacity").Return(minChan)
	mockSufficientLiquidity(mockLM)
	mockLM.On("ReserveLsps1Order", mock.Anything, mock.Anything).Return(true, nil)
	mockOS.On("GetFeeParamsMenu", mock.Anything, mock.Anything).Return([]*core.OpeningFeeParams{{MinFeeMloki: 1000, Proportional: 100}}, nil)
	mockOS.On("CountLsps1Orders", mock.Anything, mock.Anything, mock.Anything).Return(0, nil)
	mockLC.On("AddHoldInvoice", mock.Anything, mock.Anything).Return(&invoicesrpc.AddHoldInvoiceResp{PaymentRequest: "lnbc..."}, nil)
	mockNotif.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, "CREATED", "", "").Return(true, nil)

	cfg := Config{Lsps1MaxPendingOrders: 10}
	server := NewHandlers(cfg, mockLC, mockNS, mockOS, mockLM, mockNotif, nil)

	reqData := map[string]interface{}{
		"client_balance_loki":            "100000",
		"lsp_balance_loki":               "0",
		"required_channel_confirmations": 0,
		"funding_confirms_within_blocks": 1,
		"channel_expiry_blocks":          144,
	}
	paramsBytes, _ := json.Marshal(reqData)
	req := &JsonRpcRequest{Id: 1, Method: "lsps1.create_order", Params: paramsBytes, JsonRpc: "2.0"}

	resp := server.handleLsps1CreateOrder(req, []byte("peer_id"))

	require.Nil(t, resp.Error, "must succeed using ONE config snapshot (configA's generous ceiling) for the whole request -- a reintroduced double-load bug would instead reject using configB's much lower ceiling")
}

// TestHandleLsps1CreateOrder_PersistsExpiresAtAndReservedAmount is the
// regression test for T7-1/T7-11: a persisted order must carry its own
// ExpiresAt (computed once from live config, not re-derived later -- how
// F-19 happened) and ReservedAmountLoki equal to the true committed
// capacity (F-24), not lsp_balance_loki.
func TestHandleLsps1CreateOrder_PersistsExpiresAtAndReservedAmount(t *testing.T) {
	mockLM := new(MockLiquidityManager)
	mockNS := new(MockNodesService)
	mockOS := new(MockOpeningService)
	mockLC := new(MockLndClient)
	mockNotif := new(MockNotificationService)

	const (
		lspBalance    = uint64(50_000)
		clientBalance = uint64(10_000)
		padding       = int64(2_000)
	)
	minChan := uint64(20_000)
	wantCapacity := lspBalance + clientBalance + uint64(padding)

	minReqConf := uint16(0)
	minFundingConf := uint16(1)
	supportsZeroReserve := true
	maxExpiry := uint32(201600)
	orderExpirySeconds := uint32(3600)
	maxClient := uint64(1_000_000)
	maxLsp := uint64(1_000_000)
	maxChan := uint64(1_000_000)

	mockNS.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
		AdditionalChannelCapacity:            padding,
		Lsps1MinRequiredChannelConfirmations: &minReqConf,
		Lsps1MinFundingConfirmsWithinBlocks:  &minFundingConf,
		Lsps1SupportsZeroChannelReserve:      &supportsZeroReserve,
		Lsps1MaxChannelExpiryBlocks:          &maxExpiry,
		Lsps1OrderExpirySeconds:              &orderExpirySeconds,
		Lsps1MinInitialClientBalanceLoki:     new(uint64),
		Lsps1MaxInitialClientBalanceLoki:     &maxClient,
		Lsps1MinInitialLspBalanceLoki:        new(uint64),
		Lsps1MaxInitialLspBalanceLoki:        &maxLsp,
		Lsps1MinChannelBalanceLoki:           &minChan,
		Lsps1MaxChannelBalanceLoki:           &maxChan,
	})}})
	mockNS.On("GetMinChannelCapacity").Return(minChan)
	mockSufficientLiquidity(mockLM)
	mockOS.On("GetFeeParamsMenu", mock.Anything, mock.Anything).Return([]*core.OpeningFeeParams{{MinFeeMloki: 1000, Proportional: 100}}, nil)
	mockOS.On("CountLsps1Orders", mock.Anything, mock.Anything, mock.Anything).Return(0, nil)
	mockLC.On("AddHoldInvoice", mock.Anything, mock.Anything).Return(&invoicesrpc.AddHoldInvoiceResp{PaymentRequest: "lnbc..."}, nil)
	mockNotif.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, "CREATED", "", "").Return(true, nil)

	before := time.Now()
	var captured *core.Lsps1Order
	mockLM.On("ReserveLsps1Order", mock.Anything, mock.MatchedBy(func(o *core.Lsps1Order) bool {
		captured = o
		return true
	})).Return(true, nil)

	cfg := Config{Lsps1MaxPendingOrders: 10}
	server := NewHandlers(cfg, mockLC, mockNS, mockOS, mockLM, mockNotif, nil)

	reqData := map[string]interface{}{
		"client_balance_loki":            "10000",
		"lsp_balance_loki":               "50000",
		"required_channel_confirmations": 0,
		"funding_confirms_within_blocks": 1,
		"channel_expiry_blocks":          144,
	}
	paramsBytes, _ := json.Marshal(reqData)
	req := &JsonRpcRequest{Id: 1, Method: "lsps1.create_order", Params: paramsBytes, JsonRpc: "2.0"}

	resp := server.handleLsps1CreateOrder(req, []byte("peer_id"))
	require.Nil(t, resp.Error)
	require.NotNil(t, captured, "RegisterLsps1Order must have been called")

	assert.Equal(t, wantCapacity, captured.ReservedAmountLoki, "must reserve the real committed capacity, not lsp_balance_loki alone")
	assert.WithinDuration(t, before.Add(time.Duration(orderExpirySeconds)*time.Second), captured.ExpiresAt, 5*time.Second,
		"ExpiresAt must be computed once, at creation, from the configured order-expiry duration")
}
