package lsps

import (
	"encoding/json"
	"testing"

	"github.com/flokiorg/flnd/lnrpc/invoicesrpc"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newLsps1FleetCeilingTestServer builds a full, otherwise-successful
// lsps1.create_order server (same node config shape as the H-7
// regression test in handler_lsps1_test.go) so fleet-ceiling and
// burst-limit tests can prove rejection happens before any DB/RPC side
// effect, not just that the request eventually fails for some unrelated
// reason.
func newLsps1FleetCeilingTestServer(t *testing.T, cfg Config) (*Handlers, *MockOpeningStore, *MockLndClient, *MockLiquidityManager) {
	t.Helper()
	mockStore := new(MockOpeningStore)
	mockLnd := new(MockLndClient)
	mockNodes := new(MockNodesService)
	mockLiquidity := new(MockLiquidityManager)
	mockNotif := new(MockNotificationService)

	minClient := uint64(0)
	maxClient := uint64(100_000_000)
	minLsp := uint64(0)
	maxLsp := uint64(100_000_000)
	minChan := uint64(20000)
	maxChan := uint64(100_000_000)
	minReqConf := uint16(0)
	minFundingConf := uint16(1)
	supportsZeroReserve := true
	maxExpiry := uint32(201600)
	orderExpiry := uint32(86400)

	mockNodes.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
		FeeRate:                              0.000001,
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
	})}}, nil)
	mockNodes.On("GetMinChannelCapacity").Return(uint64(20000))
	mockStore.On("GetFeeParamsMenu", mock.Anything, mock.Anything).Return([]*core.OpeningFeeParams{}, nil)
	mockStore.On("CountLsps1Orders", mock.Anything, string(Lsps1OrderStateCreated), mock.Anything).Return(0, nil)
	// A successful create_order fires this in a background goroutine
	// (H-12-adjacent); .Maybe() since not every test case here reaches
	// success, but .Return(...) is still required -- a .Maybe() mock
	// without one still panics if the goroutine actually invokes it.
	mockNotif.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return(true, nil)

	server := NewHandlers(cfg, mockLnd, mockNodes, mockStore, mockLiquidity, mockNotif, nil)
	return server, mockStore, mockLnd, mockLiquidity
}

// lsps1CreateOrderRequest returns a request whose channelCapacity
// (client+lsp balance) is exactly 6_000_000 loki -- comfortably clear of
// every business/physical floor set up by newLsps1FleetCeilingTestServer,
// so tests can reason about fleet-ceiling thresholds against a known
// number.
func lsps1CreateOrderRequest() Lsps1CreateOrderRequest {
	return Lsps1CreateOrderRequest{
		ClientBalanceLoki:            "1000000",
		LspBalanceLoki:               "5000000",
		FundingConfirmsWithinBlocks:  1,
		RequiredChannelConfirmations: 0,
		ChannelExpiryBlocks:          201600,
	}
}

// TestHandleLsps1CreateOrder_F17_FleetCeilingExceeded_Rejected is the
// regression test for the fleet-wide-ceiling half of F-17/T7-7: a
// per-peer standing cap alone is Sybil-able (a fresh BOLT8 keypair costs
// nothing), so this global ceiling must reject once total reserved
// liquidity plus this request's own capacity would exceed the configured
// ceiling -- and must do so before ever touching AddHoldInvoice or
// CheckOnChainLiquidity (a cheap, non-atomic fast-fail, same as the
// existing CheckOnChainLiquidity check it sits next to).
func TestHandleLsps1CreateOrder_F17_FleetCeilingExceeded_Rejected(t *testing.T) {
	server, _, mockLnd, mockLiquidity := newLsps1FleetCeilingTestServer(t, Config{
		Lsps1MaxPendingOrders:               5,
		MaxGlobalPendingReservationsPerNode: 5_000_000, // below this request's 6_000_000 capacity
	})
	// Liquidity itself would be sufficient (10M confirmed, nothing else
	// reserved) -- isolates the fleet ceiling as the specific rejection
	// reason, not a coincidental liquidity shortfall.
	mockLiquidity.On("GetLiquiditySnapshot", mock.Anything).Return(LiquiditySnapshot{ConfirmedBalanceLoki: 10_000_000}, nil)

	req := lsps1CreateOrderRequest()
	paramsBytes, _ := json.Marshal(req)
	resp := server.Route(&JsonRpcRequest{Id: "1", Method: "lsps1.create_order", Params: paramsBytes}, []byte("peer_id"))

	require.NotNil(t, resp.Error)
	mockLnd.AssertNotCalled(t, "AddHoldInvoice", mock.Anything, mock.Anything)
	mockLiquidity.AssertNotCalled(t, "ReserveLsps1Order", mock.Anything, mock.Anything)
}

// TestHandleLsps1CreateOrder_F17_FleetCeilingNotExceeded_Proceeds proves
// the ceiling doesn't false-positive-reject legitimate traffic still
// comfortably under the configured limit.
func TestHandleLsps1CreateOrder_F17_FleetCeilingNotExceeded_Proceeds(t *testing.T) {
	server, _, mockLnd, mockLiquidity := newLsps1FleetCeilingTestServer(t, Config{
		Lsps1MaxPendingOrders:               5,
		MaxGlobalPendingReservationsPerNode: 100_000_000,
	})
	mockSufficientLiquidity(mockLiquidity)
	mockLiquidity.On("ReserveLsps1Order", mock.Anything, mock.Anything).Return(true, nil)
	mockLnd.On("AddHoldInvoice", mock.Anything, mock.Anything).Return(&invoicesrpc.AddHoldInvoiceResp{PaymentRequest: "lnbc1..."}, nil)

	req := lsps1CreateOrderRequest()
	paramsBytes, _ := json.Marshal(req)
	resp := server.Route(&JsonRpcRequest{Id: "1", Method: "lsps1.create_order", Params: paramsBytes}, []byte("peer_id"))

	require.Nil(t, resp.Error)
}

// TestHandleLsps1CreateOrder_F17_FleetCeilingZero_MeansDisabled proves the
// documented "zero means no fleet ceiling enforced" contract (handlers.go's
// Config.MaxGlobalPendingReservationsPerNode doc comment): even with a
// huge already-reserved total that would exceed almost any real ceiling,
// leaving MaxGlobalPendingReservationsPerNode at zero must still let the
// request through -- proving the ceiling comparison itself is skipped,
// not just coincidentally not triggered.
func TestHandleLsps1CreateOrder_F17_FleetCeilingZero_MeansDisabled(t *testing.T) {
	server, _, mockLnd, mockLiquidity := newLsps1FleetCeilingTestServer(t, Config{
		Lsps1MaxPendingOrders: 5,
		// MaxGlobalPendingReservationsPerNode left at its zero value.
	})
	mockLiquidity.On("GetLiquiditySnapshot", mock.Anything).Return(LiquiditySnapshot{
		ConfirmedBalanceLoki: 1_010_000_000,
		ReservedLoki:         999_000_000, // huge -- would blow past any real ceiling
	}, nil) // 11_000_000 available, comfortably above this request's 6_000_000
	mockLiquidity.On("ReserveLsps1Order", mock.Anything, mock.Anything).Return(true, nil)
	mockLnd.On("AddHoldInvoice", mock.Anything, mock.Anything).Return(&invoicesrpc.AddHoldInvoiceResp{PaymentRequest: "lnbc1..."}, nil)

	req := lsps1CreateOrderRequest()
	paramsBytes, _ := json.Marshal(req)
	resp := server.Route(&JsonRpcRequest{Id: "1", Method: "lsps1.create_order", Params: paramsBytes}, []byte("peer_id"))

	require.Nil(t, resp.Error)
}

// TestHandleLsps1CreateOrder_F17_BurstLimitExceeded_RejectsBeforeAnyWork
// proves the burst limiter is wired as the literal first statement: once
// a peer exceeds its burst allowance, the request is rejected without
// ever reaching param validation's own side effects (no AddHoldInvoice
// call for the rejected call).
func TestHandleLsps1CreateOrder_F17_BurstLimitExceeded_RejectsBeforeAnyWork(t *testing.T) {
	server, _, mockLnd, mockLiquidity := newLsps1FleetCeilingTestServer(t, Config{Lsps1MaxPendingOrders: 1000})
	mockSufficientLiquidity(mockLiquidity)
	mockLiquidity.On("ReserveLsps1Order", mock.Anything, mock.Anything).Return(true, nil)
	mockLnd.On("AddHoldInvoice", mock.Anything, mock.Anything).Return(&invoicesrpc.AddHoldInvoiceResp{PaymentRequest: "lnbc1..."}, nil)

	req := lsps1CreateOrderRequest()
	paramsBytes, _ := json.Marshal(req)
	jreq := &JsonRpcRequest{Id: "1", Method: "lsps1.create_order", Params: paramsBytes}
	peer := []byte("burst_peer_lsps1")

	for i := range defaultBurstLimit {
		resp := server.Route(jreq, peer)
		require.Nil(t, resp.Error, "call %d should be within the burst allowance", i+1)
	}

	resp := server.Route(jreq, peer)
	require.NotNil(t, resp.Error, "the call exceeding the burst allowance must be rejected")
	require.Equal(t, -32603, resp.Error.Code)

	otherPeerResp := server.Route(jreq, []byte("a_different_peer"))
	require.Nil(t, otherPeerResp.Error, "a different peer must not be affected by burst_peer_lsps1's window")
}

// TestLsps2Buy_F17_BurstLimitExceeded_RejectsBeforePromiseVerification is
// the regression test for the senior-dev review's explicit placement
// requirement: the burst check must be the literal first statement in
// lsps2.buy, before promise-signature verification -- a real
// cryptographic operation this method otherwise runs unconditionally on
// every call. Proven here by never giving ValidateOpeningFeeParams a
// matching expectation and asserting it's never invoked once the burst
// limit trips.
func TestLsps2Buy_F17_BurstLimitExceeded_RejectsBeforePromiseVerification(t *testing.T) {
	server, mockNodes, mockOpening, mockLiquidity, _ := newLsps2ReservationTestServer(t, 20_000)
	mockSufficientLiquidity(mockLiquidity)
	mockLiquidity.On("ReserveLsps2Buy", mock.Anything, mock.Anything).Return(true, nil)
	mockOpening.On("CountLsps2PendingRegistrations", mock.Anything).Return(0, nil)

	validUntil := "2026-01-01T00:00:00Z"
	req := buyRequest(t, "1000", 100, validUntil, "1000", "100000000", "50000")
	peer := []byte("burst_peer_lsps2")

	for i := range defaultBurstLimit {
		resp := server.Route(req, peer)
		require.Nil(t, resp.Error, "call %d should be within the burst allowance", i+1)
	}

	resp := server.Route(req, peer)
	require.NotNil(t, resp.Error, "the call exceeding the burst allowance must be rejected")
	require.Equal(t, -32603, resp.Error.Code)
	// GetNodes is called once per call by Route's protocol gate (is LSPS2
	// offered, F8-Q1) and once more by promise verification (inside the
	// loop that calls ValidateOpeningFeeParams for each node). The
	// burst-limited call passes the gate but must not reach verification:
	// 2 per allowed call plus 1 for the rejected one. Had it reached
	// verification, the count would be one higher.
	mockNodes.AssertNumberOfCalls(t, "GetNodes", 2*defaultBurstLimit+1)
}

// TestLsps2Buy_F17_StandingCapExceeded_Rejected is the regression test for
// the per-peer standing-cap half of F-17/T7-7, mirroring
// lsps1.create_order's pre-existing CountLsps1Orders check: before this
// fix, lsps2.buy had no durable, restart-surviving per-peer cap at all.
func TestLsps2Buy_F17_StandingCapExceeded_Rejected(t *testing.T) {
	server, _, mockOpening, mockLiquidity, _ := newLsps2ReservationTestServer(t, 20_000)
	server.config.Lsps2MaxPendingRegistrations = 3
	mockOpening.On("CountLsps2PendingRegistrations", mock.Anything).Return(3, nil) // already at the cap

	req := buyRequest(t, "1000", 100, "2026-01-01T00:00:00Z", "1000", "100000000", "50000")
	resp := server.Route(req, []byte("peer_id"))

	require.NotNil(t, resp.Error)
	mockLiquidity.AssertNotCalled(t, "GetLiquiditySnapshot", mock.Anything)
	mockLiquidity.AssertNotCalled(t, "ReserveLsps2Buy", mock.Anything, mock.Anything)
}

// TestLsps2Buy_F17_StandingCapNotExceeded_Proceeds proves the standing cap
// doesn't false-positive-reject a peer still under its limit.
func TestLsps2Buy_F17_StandingCapNotExceeded_Proceeds(t *testing.T) {
	server, _, mockOpening, mockLiquidity, _ := newLsps2ReservationTestServer(t, 20_000)
	server.config.Lsps2MaxPendingRegistrations = 3
	mockOpening.On("CountLsps2PendingRegistrations", mock.Anything).Return(2, nil) // one slot free
	mockSufficientLiquidity(mockLiquidity)
	mockLiquidity.On("ReserveLsps2Buy", mock.Anything, mock.Anything).Return(true, nil)

	req := buyRequest(t, "1000", 100, "2026-01-01T00:00:00Z", "1000", "100000000", "50000")
	resp := server.Route(req, []byte("peer_id"))

	require.Nil(t, resp.Error)
}

// TestLsps2Buy_F17_StandingCapZero_MeansDisabled proves the standing cap
// is opt-in, mirroring the fleet ceiling's "zero means disabled" contract:
// existing deployments that never set Lsps2MaxPendingRegistrations must
// see no behavior change, including no CountLsps2PendingRegistrations call.
func TestLsps2Buy_F17_StandingCapZero_MeansDisabled(t *testing.T) {
	server, _, mockOpening, mockLiquidity, _ := newLsps2ReservationTestServer(t, 20_000)
	// Lsps2MaxPendingRegistrations left at its zero value.
	mockSufficientLiquidity(mockLiquidity)
	mockLiquidity.On("ReserveLsps2Buy", mock.Anything, mock.Anything).Return(true, nil)

	req := buyRequest(t, "1000", 100, "2026-01-01T00:00:00Z", "1000", "100000000", "50000")
	resp := server.Route(req, []byte("peer_id"))

	require.Nil(t, resp.Error)
	mockOpening.AssertNotCalled(t, "CountLsps2PendingRegistrations", mock.Anything)
}

// TestLsps2Buy_F17_FleetCeilingExceeded_Rejected is the LSPS2 counterpart
// of TestHandleLsps1CreateOrder_F17_FleetCeilingExceeded_Rejected -- same
// Sybil-resistance rationale, checked against requiredCapacity (the real
// committed capacity, F-16/F-24) rather than the raw payment size.
func TestLsps2Buy_F17_FleetCeilingExceeded_Rejected(t *testing.T) {
	server, _, mockOpening, mockLiquidity, _ := newLsps2ReservationTestServer(t, 20_000)
	server.config.MaxGlobalPendingReservationsPerNode = 1 // any real request will exceed this
	mockOpening.On("CountLsps2PendingRegistrations", mock.Anything).Return(0, nil)
	// Liquidity itself is comfortably sufficient -- isolates the fleet
	// ceiling (1) as the specific rejection reason, not a coincidental
	// liquidity shortfall.
	mockSufficientLiquidity(mockLiquidity)

	req := buyRequest(t, "1000", 100, "2026-01-01T00:00:00Z", "1000", "100000000", "50000")
	resp := server.Route(req, []byte("peer_id"))

	require.NotNil(t, resp.Error)
	mockLiquidity.AssertNotCalled(t, "ReserveLsps2Buy", mock.Anything, mock.Anything)
}

// TestLsps2Buy_F17_FleetCeilingZero_MeansDisabled mirrors the LSPS1 case:
// even with a huge already-reserved total that would exceed almost any
// real ceiling, zero must still mean no fleet ceiling enforced.
func TestLsps2Buy_F17_FleetCeilingZero_MeansDisabled(t *testing.T) {
	server, _, mockOpening, mockLiquidity, _ := newLsps2ReservationTestServer(t, 20_000)
	mockOpening.On("CountLsps2PendingRegistrations", mock.Anything).Return(0, nil)
	mockLiquidity.On("GetLiquiditySnapshot", mock.Anything).Return(LiquiditySnapshot{
		ConfirmedBalanceLoki: 1_010_000_000,
		ReservedLoki:         999_000_000, // huge -- would blow past any real ceiling
	}, nil)
	mockLiquidity.On("ReserveLsps2Buy", mock.Anything, mock.Anything).Return(true, nil)

	req := buyRequest(t, "1000", 100, "2026-01-01T00:00:00Z", "1000", "100000000", "50000")
	resp := server.Route(req, []byte("peer_id"))

	require.Nil(t, resp.Error)
}

// TestLsps2GetInfo_ReadBurstLimitExceeded_RejectsBeforeGetNodes (R-LSP-5)
// pins the read limiter at the top of Route: unlike the mutating burst
// limiter, it guards every method -- including the unauthenticated read
// lsps2.get_info, whose menu signs an ECDSA promise per fee setting per token
// per node. A peer past the read allowance is rejected (-32603) before Route's
// protocol gate even calls GetNodes, so none of that signing runs.
func TestLsps2GetInfo_ReadBurstLimitExceeded_RejectsBeforeGetNodes(t *testing.T) {
	mockNodes := new(MockNodesService)
	nodeCfg := &config.NodeConfig{JitEnabled: true, MinPaymentSizeMloki: 20_000_000}
	require.NoError(t, nodeCfg.Validate())
	mockNodes.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(nodeCfg)}}).Maybe()
	mockOpening := new(MockOpeningService)
	mockOpening.On("GetFeeParamsMenu", mock.Anything, mock.Anything).Return([]*core.OpeningFeeParams{}, nil).Maybe()
	server := NewHandlers(Config{}, new(MockLndClient), mockNodes, mockOpening, new(MockLiquidityManager), new(MockNotificationService), nil)

	req := &JsonRpcRequest{Id: "1", Method: "lsps2.get_info"}
	peer := []byte("read_flood_peer")

	countGetNodes := func() int {
		n := 0
		for _, c := range mockNodes.Calls {
			if c.Method == "GetNodes" {
				n++
			}
		}
		return n
	}

	for i := range defaultReadLimit {
		resp := server.Route(req, peer)
		if resp.Error != nil {
			require.NotEqual(t, -32603, resp.Error.Code, "call %d is within the read allowance", i+1)
		}
	}
	require.Positive(t, countGetNodes(), "the allowed reads reached GetNodes")
	before := countGetNodes()

	resp := server.Route(req, peer)
	require.NotNil(t, resp.Error, "the read past the allowance is rejected")
	require.Equal(t, -32603, resp.Error.Code)
	require.Equal(t, before, countGetNodes(), "a read-limited call is rejected before GetNodes")

	// A different peer keeps its own window.
	other := server.Route(req, []byte("another_peer"))
	if other.Error != nil {
		require.NotEqual(t, -32603, other.Error.Code, "a different peer is not affected by read_flood_peer's window")
	}
}
