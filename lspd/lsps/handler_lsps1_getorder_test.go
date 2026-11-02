package lsps

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newGetOrderTestServer builds a *Handlers with a NodeConfig fixture that
// satisfies getLsps1ConfigValues (isOrderExpired/handleLsps1GetOrder both
// call it), with orderExpirySeconds configurable per test.
func newGetOrderTestServer(t *testing.T, mockOS *MockOpeningService, orderExpirySeconds uint32) *Handlers {
	t.Helper()
	mockNS := new(MockNodesService)
	minReqConf := uint16(0)
	minFundingConf := uint16(1)
	supportsZeroReserve := true
	maxExpiry := uint32(201600)
	minClient, maxClient := uint64(0), uint64(100_000_000)
	minLsp, maxLsp := uint64(0), uint64(100_000_000)
	minChan, maxChan := uint64(20000), uint64(100_000_000)

	mockNS.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
		Lsps1MinRequiredChannelConfirmations: &minReqConf,
		Lsps1MinFundingConfirmsWithinBlocks:  &minFundingConf,
		Lsps1SupportsZeroChannelReserve:      &supportsZeroReserve,
		Lsps1MaxChannelExpiryBlocks:          &maxExpiry,
		Lsps1OrderExpirySeconds:              &orderExpirySeconds,
		Lsps1MinInitialClientBalanceLoki:     &minClient,
		Lsps1MaxInitialClientBalanceLoki:     &maxClient,
		Lsps1MinInitialLspBalanceLoki:        &minLsp,
		Lsps1MaxInitialLspBalanceLoki:        &maxLsp,
		Lsps1MinChannelBalanceLoki:           &minChan,
		Lsps1MaxChannelBalanceLoki:           &maxChan,
	})}}).Maybe()

	return NewHandlers(Config{}, new(MockLndClient), mockNS, mockOS, new(MockLiquidityManager), new(MockNotificationService), nil)
}

func TestHandleLsps1GetInfo(t *testing.T) {
	mockOS := new(MockOpeningService)
	mockOS.On("GetFeeParamsMenu", "token-a", mock.Anything).Return([]*core.OpeningFeeParams{
		{MinFeeMloki: 1000, Proportional: 100, ValidUntil: "2099-01-01T00:00:00.000Z"},
	}, nil)
	server := newGetOrderTestServer(t, mockOS, 3600)
	// newGetOrderTestServer's NodesService fixture has no Tokens, so
	// override with one that does, to exercise the fee-params-gathering
	// loop rather than just the (already-covered-elsewhere) empty case.
	mockNS := new(MockNodesService)
	mockNS.On("GetNodes").Return([]*core.Node{{
		Tokens: []string{"token-a"},
		NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
			Lsps1MinRequiredChannelConfirmations: uint16Ptr(1),
			Lsps1MinFundingConfirmsWithinBlocks:  uint16Ptr(2),
			Lsps1SupportsZeroChannelReserve:      boolPtr(true),
			Lsps1MaxChannelExpiryBlocks:          uint32Ptr(201600),
			Lsps1OrderExpirySeconds:              uint32Ptr(3600),
			Lsps1MinInitialClientBalanceLoki:     uint64Ptr(0),
			Lsps1MaxInitialClientBalanceLoki:     uint64Ptr(100_000_000),
			Lsps1MinInitialLspBalanceLoki:        uint64Ptr(0),
			Lsps1MaxInitialLspBalanceLoki:        uint64Ptr(100_000_000),
			Lsps1MinChannelBalanceLoki:           uint64Ptr(20000),
			Lsps1MaxChannelBalanceLoki:           uint64Ptr(100_000_000),
		}),
	}})
	server.nodesService = mockNS

	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`)}
	resp := server.handleLsps1GetInfo(req)

	require.Nil(t, resp.Error)
	result, ok := resp.Result.(Lsps1GetInfoResponse)
	require.True(t, ok)
	assert.Equal(t, []string{"lsps1.create_order", "lsps1.get_order"}, result.Options)
	require.Len(t, result.OpeningFeeParams, 1)
	assert.Equal(t, "1000", result.OpeningFeeParams[0].MinFeeMloki)
	assert.EqualValues(t, 1, result.MinRequiredChannelConfirmations)
	assert.True(t, result.SupportsZeroChannelReserve)
	assert.Equal(t, "20000", result.MinChannelBalanceLoki)
}

func uint16Ptr(v uint16) *uint16 { return &v }
func uint32Ptr(v uint32) *uint32 { return &v }
func uint64Ptr(v uint64) *uint64 { return &v }
func boolPtr(v bool) *bool       { return &v }

func TestIsOrderExpired(t *testing.T) {
	assert.False(t, isOrderExpired(time.Now().Add(time.Hour)), "an expires_at in the future must not be expired")
	assert.True(t, isOrderExpired(time.Now().Add(-time.Hour)), "an expires_at in the past must be expired")
}

func TestHandleLsps1GetOrder_InvalidParams(t *testing.T) {
	server := newGetOrderTestServer(t, new(MockOpeningService), 3600)
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: json.RawMessage(`not-json`)}

	resp := server.handleLsps1GetOrder(req)
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32602, resp.Error.Code)
}

func TestHandleLsps1GetOrder_NotFound(t *testing.T) {
	mockOS := new(MockOpeningService)
	mockOS.On("GetLsps1Order", "missing-order").Return(nil, assert.AnError)
	server := newGetOrderTestServer(t, mockOS, 3600)

	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: json.RawMessage(`{"order_id":"missing-order"}`)}
	resp := server.handleLsps1GetOrder(req)

	require.NotNil(t, resp.Error)
	assert.Equal(t, 101, resp.Error.Code)
}

func TestHandleLsps1GetOrder_ActiveCreatedOrder(t *testing.T) {
	mockOS := new(MockOpeningService)
	order := &core.Lsps1Order{
		OrderId:      "order-active",
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(time.Hour),
		OrderState:   string(Lsps1OrderStateCreated),
		PaymentState: "EXPECT_PAYMENT",
		Token:        "tok",
	}
	mockOS.On("GetLsps1Order", "order-active").Return(order, nil)
	server := newGetOrderTestServer(t, mockOS, 3600) // 1 hour, well in the future

	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: json.RawMessage(`{"order_id":"order-active"}`)}
	resp := server.handleLsps1GetOrder(req)

	require.Nil(t, resp.Error)
	result, ok := resp.Result.(Lsps1CreateOrderResponse)
	require.True(t, ok)
	assert.Equal(t, "order-active", result.OrderId)
	assert.Equal(t, string(Lsps1OrderStateCreated), result.OrderState, "a non-expired CREATED order must stay CREATED")
	assert.Nil(t, result.Channel, "an order with no funding_txid must report a nil channel")
}

// TestHandleLsps1GetOrder_ExpiredCreatedOrder_StateOverridden covers the
// override path: a CREATED order past its expiry window is expired on read
// (expiry is computed on read rather than via a background sweep touching
// every row). FLSPD-0020: the internal EXPIRED state must be mapped to the
// blip-0051 wire enums before it reaches the client -- order_state=FAILED
// (terminal non-completion) and payment.bolt11.state=REFUNDED (blip-0051
// §3.1's expiry outcome) -- never the non-spec literal "EXPIRED".
func TestHandleLsps1GetOrder_ExpiredCreatedOrder_StateOverridden(t *testing.T) {
	mockOS := new(MockOpeningService)
	order := &core.Lsps1Order{
		OrderId:      "order-expired",
		CreatedAt:    time.Now().Add(-2 * time.Hour),
		ExpiresAt:    time.Now().Add(-time.Hour),
		OrderState:   string(Lsps1OrderStateCreated),
		PaymentState: "EXPECT_PAYMENT",
	}
	mockOS.On("GetLsps1Order", "order-expired").Return(order, nil)
	server := newGetOrderTestServer(t, mockOS, 3600) // 1 hour expiry, order is 2h old

	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: json.RawMessage(`{"order_id":"order-expired"}`)}
	resp := server.handleLsps1GetOrder(req)

	require.Nil(t, resp.Error)
	result, ok := resp.Result.(Lsps1CreateOrderResponse)
	require.True(t, ok)
	assert.Equal(t, string(Lsps1OrderStateFailed), result.OrderState, "an expired order must report the spec FAILED, not the non-spec EXPIRED")
	assert.Equal(t, string(Lsps1PaymentStateRefunded), result.Payment.Bolt11.State, "an expired order's payment must report the spec REFUNDED, not the non-spec EXPIRED")
}

// TestHandleLsps1GetOrder_NeverReturnsNonSpecEnum is the FLSPD-0020
// regression test: no matter the internal order_state / payment_state, the
// get_order response's order_state is always in {CREATED,COMPLETED,FAILED}
// and payment.bolt11.state is always in {EXPECT_PAYMENT,HOLD,PAID,REFUNDED}.
// The internal EXPIRED/FAILED/PAID values that used to leak onto the wire are
// the ones a strict LSPS1 client would fail to parse.
func TestHandleLsps1GetOrder_NeverReturnsNonSpecEnum(t *testing.T) {
	specOrderStates := map[string]bool{"CREATED": true, "COMPLETED": true, "FAILED": true}
	specPaymentStates := map[string]bool{"EXPECT_PAYMENT": true, "HOLD": true, "PAID": true, "REFUNDED": true}

	cases := []struct {
		name         string
		orderState   string
		paymentState string
	}{
		{"internal PAID", string(Lsps1OrderStatePaid), string(Lsps1PaymentStatePaid)},
		{"swept EXPIRED order", string(Lsps1OrderStateExpired), string(Lsps1PaymentStateExpectPayment)},
		{"EXPIRED payment", string(Lsps1OrderStateExpired), string(Lsps1PaymentStateExpired)},
		{"FAILED payment", string(Lsps1OrderStateFailed), string(Lsps1PaymentStateFailed)},
		{"HOLD payment", string(Lsps1OrderStateCreated), string(Lsps1PaymentStateHold)},
		{"completed order", string(Lsps1OrderStateCompleted), string(Lsps1PaymentStatePaid)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockOS := new(MockOpeningService)
			order := &core.Lsps1Order{
				OrderId:      "order-enum-" + tc.name,
				CreatedAt:    time.Now(),
				ExpiresAt:    time.Now().Add(time.Hour), // not expired-on-read; test the stored state directly
				OrderState:   tc.orderState,
				PaymentState: tc.paymentState,
			}
			mockOS.On("GetLsps1Order", order.OrderId).Return(order, nil)
			server := newGetOrderTestServer(t, mockOS, 3600)

			req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: json.RawMessage(`{"order_id":"` + order.OrderId + `"}`)}
			resp := server.handleLsps1GetOrder(req)

			require.Nil(t, resp.Error)
			result, ok := resp.Result.(Lsps1CreateOrderResponse)
			require.True(t, ok)
			assert.Truef(t, specOrderStates[result.OrderState], "order_state %q is not a blip-0051 wire enum value", result.OrderState)
			assert.Truef(t, specPaymentStates[result.Payment.Bolt11.State], "payment.bolt11.state %q is not a blip-0051 wire enum value", result.Payment.Bolt11.State)
		})
	}
}

// TestHandleLsps1GetOrder_UsesPersistedExpiresAt_NotLiveConfig is the
// regression test for the F-19 read-path gap: get_order must report the
// order's own frozen expires_at, not recompute created_at + the
// CURRENTLY configured Lsps1OrderExpirySeconds. Here the order's own
// persisted ExpiresAt says "still active", while the server's live config
// (a much shorter duration than when the order was created) would have
// called it expired if get_order still recomputed from config -- proving
// the two are no longer coupled.
func TestHandleLsps1GetOrder_UsesPersistedExpiresAt_NotLiveConfig(t *testing.T) {
	mockOS := new(MockOpeningService)
	order := &core.Lsps1Order{
		OrderId:      "order-config-changed",
		CreatedAt:    time.Now().Add(-2 * time.Hour),
		ExpiresAt:    time.Now().Add(time.Hour), // order's own window: still active
		OrderState:   string(Lsps1OrderStateCreated),
		PaymentState: "EXPECT_PAYMENT",
	}
	mockOS.On("GetLsps1Order", "order-config-changed").Return(order, nil)
	// Live config now says 1 hour -- if get_order still recomputed
	// created_at(-2h)+1h, that's already in the past (expired). The
	// persisted ExpiresAt (+1h from now) must win instead.
	server := newGetOrderTestServer(t, mockOS, 3600)

	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: json.RawMessage(`{"order_id":"order-config-changed"}`)}
	resp := server.handleLsps1GetOrder(req)

	require.Nil(t, resp.Error)
	result, ok := resp.Result.(Lsps1CreateOrderResponse)
	require.True(t, ok)
	assert.Equal(t, string(Lsps1OrderStateCreated), result.OrderState, "must stay CREATED per the order's own persisted ExpiresAt, regardless of the currently configured expiry duration")
	assert.WithinDuration(t, order.ExpiresAt, mustParseRFC3339(t, result.Payment.Bolt11.ExpiresAt), time.Second, "reported ExpiresAt must be the order's own persisted field")
}

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return parsed
}

// TestHandleLsps1GetOrder_PaidOrder_MapsToCreatedForSpecCompliance covers
// the internal-PAID-state-to-spec-CREATED-state mapping: LSPS1 only
// defines CREATED/COMPLETED/FAILED, so the internal PAID bookkeeping
// state (paid but not yet channel-funded) reports back as CREATED.
func TestHandleLsps1GetOrder_PaidOrder_MapsToCreatedForSpecCompliance(t *testing.T) {
	mockOS := new(MockOpeningService)
	order := &core.Lsps1Order{
		OrderId:      "order-paid",
		CreatedAt:    time.Now(),
		OrderState:   string(Lsps1OrderStatePaid),
		PaymentState: "PAID",
	}
	mockOS.On("GetLsps1Order", "order-paid").Return(order, nil)
	server := newGetOrderTestServer(t, mockOS, 3600)

	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: json.RawMessage(`{"order_id":"order-paid"}`)}
	resp := server.handleLsps1GetOrder(req)

	require.Nil(t, resp.Error)
	result, ok := resp.Result.(Lsps1CreateOrderResponse)
	require.True(t, ok)
	assert.Equal(t, string(Lsps1OrderStateCreated), result.OrderState, "internal PAID state must map to spec-compliant CREATED")
}

// TestHandleLsps1GetOrder_FundedOrder_ReportsChannel covers the
// channel-info-populated branch: once a funding_txid is recorded, the
// response's channel field must be populated, not nil.
func TestHandleLsps1GetOrder_FundedOrder_ReportsChannel(t *testing.T) {
	mockOS := new(MockOpeningService)
	txid := "abc123"
	vout := 1
	order := &core.Lsps1Order{
		OrderId:         "order-funded",
		CreatedAt:       time.Now(),
		OrderState:      string(Lsps1OrderStateCompleted),
		PaymentState:    "PAID",
		FundingTxid:     &txid,
		FundingOutpoint: &vout,
	}
	mockOS.On("GetLsps1Order", "order-funded").Return(order, nil)
	server := newGetOrderTestServer(t, mockOS, 3600)

	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: json.RawMessage(`{"order_id":"order-funded"}`)}
	resp := server.handleLsps1GetOrder(req)

	require.Nil(t, resp.Error)
	result, ok := resp.Result.(Lsps1CreateOrderResponse)
	require.True(t, ok)
	require.NotNil(t, result.Channel)
	channelMap, ok := result.Channel.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "abc123:1", channelMap["funding_outpoint"])
}

// TestHandleLsps1GetOrder_HeldOrderWithFunding_DoesNotReportChannel is the
// regression test for a review finding: recordHeldOrderFundingAndSettle
// (settlement_service.go) now records FundingTxid *before* settlement is
// confirmed, for crash-safety -- so a hold-invoice order can have a
// non-nil FundingTxid while order_state is still PAID and payment_state is
// still HOLD (payment not yet captured). Before this fix, get_order gated
// the Channel block purely on FundingTxid!=nil, so a client polling during
// this window (or if settlement never completes) would see a
// self-contradictory response: Payment.Bolt11.State="HOLD" (not paid) next
// to a populated Channel block implying the channel is funded and ready.
func TestHandleLsps1GetOrder_HeldOrderWithFunding_DoesNotReportChannel(t *testing.T) {
	mockOS := new(MockOpeningService)
	txid := "held-order-txid"
	vout := 0
	order := &core.Lsps1Order{
		OrderId:         "order-held-funded",
		CreatedAt:       time.Now(),
		OrderState:      string(Lsps1OrderStatePaid),
		PaymentState:    "HOLD",
		FundingTxid:     &txid,
		FundingOutpoint: &vout,
	}
	mockOS.On("GetLsps1Order", "order-held-funded").Return(order, nil)
	server := newGetOrderTestServer(t, mockOS, 3600)

	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: json.RawMessage(`{"order_id":"order-held-funded"}`)}
	resp := server.handleLsps1GetOrder(req)

	require.Nil(t, resp.Error)
	result, ok := resp.Result.(Lsps1CreateOrderResponse)
	require.True(t, ok)
	assert.Equal(t, "HOLD", result.Payment.Bolt11.State, "payment must still read HOLD -- not yet captured")
	assert.Nil(t, result.Channel, "must not report a funded channel while the order hasn't reached COMPLETED, even though FundingTxid is already recorded")
}
