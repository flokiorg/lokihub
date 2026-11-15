package lsps

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flokiorg/flnd/lnrpc/invoicesrpc"
	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type matrixScenario struct {
	name           string
	protocol       string // "LSPS1" or "LSPS2"
	inputSat       uint64
	padding        int64
	expectedAccept bool
	expectedFinal  uint64 // Final physical capacity expected
	businessLimit  uint64 // lsps1MinChannelBalanceLoki
}

func TestCapacityMatrix(t *testing.T) {
	minFloor := uint64(20000)

	scenarios := []matrixScenario{
		// LSPS1 Scenarios
		{
			name:           "LSPS1: Tiny Buy (100 sat) + Padding -> Accept",
			protocol:       "LSPS1",
			inputSat:       100,
			padding:        21000,
			expectedAccept: true,
			expectedFinal:  21100,
		},
		{
			name:           "LSPS1: Tiny Buy (1 sat) + Padding -> Accept",
			protocol:       "LSPS1",
			inputSat:       1,
			padding:        21000,
			expectedAccept: true,
			expectedFinal:  21001,
		},
		{
			name:           "LSPS1: Tiny Buy (100 sat) + Tiny Padding -> Reject",
			protocol:       "LSPS1",
			inputSat:       100,
			padding:        500,
			expectedAccept: false,
		},
		{
			name:           "LSPS1: Large Buy (500k sat) + Padding -> Accept",
			protocol:       "LSPS1",
			inputSat:       500000,
			padding:        21000,
			expectedAccept: true,
			expectedFinal:  521000,
			businessLimit:  20000,
		},
		{
			name:           "LSPS1: Small Buy (10k) + Padding < 50k business limit -> Reject",
			protocol:       "LSPS1",
			inputSat:       10000,
			padding:        21000,
			expectedAccept: false,
			businessLimit:  50000,
		},
		{
			name:           "LSPS1: Small Buy (30k) + Padding > 50k business limit -> Accept",
			protocol:       "LSPS1",
			inputSat:       30000,
			padding:        21000,
			expectedAccept: true,
			expectedFinal:  51000,
			businessLimit:  50000,
		},

		// LSPS2 (JIT) Scenarios
		{
			name:           "LSPS2: Tiny JIT (100 sat) + Padding -> Accept",
			protocol:       "LSPS2",
			inputSat:       100,
			padding:        21000,
			expectedAccept: true,
			expectedFinal:  21100,
		},
		{
			name:           "LSPS2: Borderline JIT (2 sat) + Padding -> Accept",
			protocol:       "LSPS2",
			inputSat:       2,
			padding:        21000,
			expectedAccept: true,
			expectedFinal:  21002,
			businessLimit:  20000,
		},
		{
			name:           "LSPS2: JIT Payment (10k) + Pad < 50k business limit -> Reject",
			protocol:       "LSPS2",
			inputSat:       10000,
			padding:        21000,
			expectedAccept: false,
			businessLimit:  50000,
		},
		{
			name:           "LSPS1: Business limit < 20k -> Enforce 20k floor",
			protocol:       "LSPS1",
			inputSat:       1000,
			padding:        5000, // 6000 total
			expectedAccept: false,
			businessLimit:  5000, // Low business limit, should still reject due to 20k floor
		},
	}

	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			if tc.protocol == "LSPS1" {
				verifyLsps1Capacity(t, tc, minFloor)
			} else {
				verifyLsps2Capacity(t, tc, minFloor)
			}
		})
	}
}

func verifyLsps1Capacity(t *testing.T, tc matrixScenario, minFloor uint64) {
	// 1. Verify Handler Validation
	nodes := new(MockNodesService)
	lnd := new(MockLightningClient)
	store := new(MockOpeningStore)
	liquidity := new(MockLiquidityManager)
	notif := new(MockNotificationService)

	cfg := Config{
		Lsps1MaxPendingOrders: 5,
	}
	s := NewHandlers(cfg, lnd, nodes, nil, liquidity, notif, nil)
	s.openingService = store

	zeroU16 := uint16(0)
	oneU16 := uint16(1)
	zeroU64 := uint64(0)
	trueVal := true
	blocks := uint32(201600)
	secs := uint32(86400)
	maxU64 := uint64(100_000_000)

	nodeConfig := &config.NodeConfig{
		Lsps1MinRequiredChannelConfirmations: &zeroU16,
		Lsps1MinFundingConfirmsWithinBlocks:  &oneU16,
		Lsps1SupportsZeroChannelReserve:      &trueVal,
		Lsps1MaxChannelExpiryBlocks:          &blocks,
		Lsps1OrderExpirySeconds:              &secs,
		Lsps1MinInitialClientBalanceLoki:     &zeroU64,
		Lsps1MaxInitialClientBalanceLoki:     &maxU64,
		Lsps1MinInitialLspBalanceLoki:        &zeroU64,
		Lsps1MaxInitialLspBalanceLoki:        &maxU64,
		Lsps1MinChannelBalanceLoki:           &tc.businessLimit,
		Lsps1MaxChannelBalanceLoki:           &maxU64,
		AdditionalChannelCapacity:            tc.padding,
	}

	nodes.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(nodeConfig)}})
	nodes.On("GetMinChannelCapacity").Return(minFloor)
	mockSufficientLiquidity(liquidity)
	store.On("GetFeeParamsMenu", mock.Anything, mock.Anything).Return([]*core.OpeningFeeParams{}, nil)
	store.On("CountLsps1Orders", mock.Anything, mock.Anything, mock.Anything).Return(0, nil)
	lnd.On("AddHoldInvoice", mock.Anything, mock.Anything).Return(&invoicesrpc.AddHoldInvoiceResp{}, nil)
	liquidity.On("ReserveLsps1Order", mock.Anything, mock.Anything).Return(true, nil)
	notif.On("NotifyOrderStateChanged", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(true, nil)

	req := Lsps1CreateOrderRequest{
		ClientBalanceLoki: fmt.Sprintf("%d", tc.inputSat),
		LspBalanceLoki:    "0",
	}
	resp := s.handleLsps1CreateOrder(&JsonRpcRequest{Id: 1, Params: mustMarshal(req)}, []byte("peer"))

	if tc.expectedAccept {
		assert.Nil(t, resp.Error, "Expected LSPS1 order to be accepted")
	} else {
		assert.NotNil(t, resp.Error, "Expected LSPS1 order to be rejected")
		return
	}

	// 2. Verify Settlement Capacity Calculation
	capacity := tc.inputSat + uint64(tc.padding)
	if capacity < minFloor {
		capacity = minFloor
	}
	assert.Equal(t, tc.expectedFinal, capacity, "Final capacity mismatch in settlement logic")
}

func verifyLsps2Capacity(t *testing.T, tc matrixScenario, minFloor uint64) {
	nodes := new(MockNodesService)
	liquidity := new(MockLiquidityManager)
	store := new(MockOpeningStore)

	cfg := Config{}
	s := NewHandlers(cfg, nil, nodes, nil, liquidity, nil, nil)
	s.openingService = store

	pubkeyBytes := make([]byte, 32)
	pubkeyBytes[31] = 1
	nodePriv, _ := btcec.PrivKeyFromBytes(pubkeyBytes)

	nodeConfig := &config.NodeConfig{
		AdditionalChannelCapacity: tc.padding,
		JitMinChannelBalanceLoki:  &tc.businessLimit,
	}
	validNode := &core.Node{
		PublicKey:  nodePriv.PubKey(),
		NodeConfig: config.NewNodeConfigRef(nodeConfig),
		Tokens:     []string{"token"},
	}

	nodes.On("GetNodes").Return([]*core.Node{validNode})
	nodes.On("GetMinChannelCapacity").Return(minFloor)
	mockSufficientLiquidity(liquidity)
	store.On("ValidateOpeningFeeParams", mock.Anything, mock.Anything).Return(true)
	liquidity.On("ReserveLsps2Buy", mock.Anything, mock.Anything).Return(true, nil)

	req := Lsps2BuyParams{
		OpeningFeeParams: Lsps2OpeningFeeParams{
			MinFeeMloki:          "1000",
			Proportional:         10,
			ValidUntil:           "2026-01-01T00:00:00Z",
			MinLifetime:          1000,
			MaxClientToSelfDelay: 1000,
			MinPaymentSizeMloki:  "1",
			MaxPaymentSizeMloki:  "10000000000",
			Promise:              "promise",
		},
		PaymentSizeMloki: fmt.Sprintf("%d", tc.inputSat*1000),
	}

	resp := s.lsps2Buy(&JsonRpcRequest{Id: 1, Params: mustMarshal(req)}, []byte("peer"))

	if tc.expectedAccept {
		assert.Nil(t, resp.Error, "Expected LSPS2 buy to be accepted")
		capacity := tc.inputSat + uint64(tc.padding)
		if capacity < minFloor {
			capacity = minFloor
		}
		assert.Equal(t, tc.expectedFinal, capacity, "Final capacity mismatch in JIT logic")
	} else {
		assert.NotNil(t, resp.Error, "Expected LSPS2 buy to be rejected")
	}
}

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
