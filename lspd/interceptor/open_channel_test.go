package interceptor

import (
	"testing"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/chain"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func newTestOutPoint() *wire.OutPoint {
	var h wire.OutPoint
	return &h
}

func baseTestConfig() *config.NodeConfig {
	return &config.NodeConfig{
		MinChannelCapacityLoki:    20_000,
		AdditionalChannelCapacity: 1_000,
		TargetConf:                6,
		FeeRate:                   0.001,
		BaseFeeMloki:              1000,
		TimeLockDelta:             144,
	}
}

// newTestInterceptor wires up an Interceptor with mocked lightning.Client /
// chain.FeeEstimator so openChannel's real logic (not a copy of its math)
// runs against controllable dependencies.
func newTestInterceptor(cfg *config.NodeConfig, client *MockLightningClient, fee *fakeFeeEstimator, store *MockStore) *Interceptor {
	if fee == nil {
		fee = &fakeFeeEstimator{estimation: fixedFeeEstimation()}
	}
	if store == nil {
		store = new(MockStore)
	}
	return &Interceptor{
		config:       config.NewNodeConfigRef(cfg),
		client:       client,
		feeEstimator: fee,
		feeStrategy:  chain.FeeStrategyHalfHour,
		store:        store,
	}
}

func TestOpenChannel_Success_NonJitUsesConfiguredRoutingFee(t *testing.T) {
	cfg := baseTestConfig()
	client := new(MockLightningClient)
	expectedOutpoint := newTestOutPoint()

	client.On("OpenChannel", mock.MatchedBy(func(req *lightning.OpenChannelRequest) bool {
		// 20_000_000 mloki / 1000 = 20000 loki + 1000 loki padding = 21000 loki
		// naive, but the reserve floor (ceil(19800/0.99) + 1000 = 21000)
		// doesn't push it any higher for this outgoing amount.
		return req.CapacityLoki == 21_000 &&
			req.ZeroConf == false &&
			req.RoutingPolicy.Ppm == uint32(cfg.FeeRate*1_000_000) &&
			req.RoutingPolicy.BaseMloki == cfg.BaseFeeMloki
	})).Return(expectedOutpoint, nil)

	store := new(MockStore)
	store.On("SetFundingTx", mock.Anything, expectedOutpoint).Return(nil)
	i := newTestInterceptor(cfg, client, nil, store)

	// outgoing < incoming (19_800_000, not 20_000_000) to represent a
	// realistic routing fee taken out before forwarding.
	outpoint, err := i.openChannel([]byte{1}, []byte{2}, 20_000_000, 19_800_000, nil, false)
	require.NoError(t, err)
	assert.Equal(t, expectedOutpoint, outpoint)
	client.AssertExpectations(t)
}

func TestOpenChannel_JitUsesZeroRoutingFeeAndZeroConf(t *testing.T) {
	cfg := baseTestConfig()
	cfg.JitEnabled = true
	client := new(MockLightningClient)
	expectedOutpoint := newTestOutPoint()

	client.On("OpenChannel", mock.MatchedBy(func(req *lightning.OpenChannelRequest) bool {
		return req.ZeroConf == true &&
			req.RoutingPolicy.Ppm == 0 &&
			req.RoutingPolicy.BaseMloki == 0 &&
			req.MinConfs != nil && *req.MinConfs == 0
	})).Return(expectedOutpoint, nil)

	store := new(MockStore)
	store.On("SetFundingTx", mock.Anything, expectedOutpoint).Return(nil)
	i := newTestInterceptor(cfg, client, nil, store)

	_, err := i.openChannel([]byte{1}, []byte{2}, 20_000_000, 20_000_000, nil, true)
	require.NoError(t, err)
	client.AssertExpectations(t)
}

func TestOpenChannel_RejectsCapacityBelowNetworkFloor(t *testing.T) {
	cfg := baseTestConfig()
	cfg.MinChannelCapacityLoki = 1_000_000 // far above what this tiny payment would produce
	client := new(MockLightningClient)
	i := newTestInterceptor(cfg, client, nil, nil)

	_, err := i.openChannel([]byte{1}, []byte{2}, 1_000, 1_000, nil, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "smaller than minimum")
	client.AssertNotCalled(t, "OpenChannel", mock.Anything)
}

func TestOpenChannel_Jit_RejectsCapacityBelowBusinessFloor(t *testing.T) {
	cfg := baseTestConfig()
	cfg.JitEnabled = true
	businessFloor := uint64(500_000)
	cfg.JitMinChannelBalanceLoki = &businessFloor
	client := new(MockLightningClient)
	i := newTestInterceptor(cfg, client, nil, nil)

	_, err := i.openChannel([]byte{1}, []byte{2}, 20_000_000, 20_000_000, nil, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "business minimum")
	client.AssertNotCalled(t, "OpenChannel", mock.Anything)
}

func TestOpenChannel_Jit_RejectsPaymentAboveMaxSize(t *testing.T) {
	cfg := baseTestConfig()
	cfg.JitEnabled = true
	cfg.JitMaxPaymentSizeMloki = 10_000_000
	client := new(MockLightningClient)
	i := newTestInterceptor(cfg, client, nil, nil)

	_, err := i.openChannel([]byte{1}, []byte{2}, 20_000_000, 20_000_000, nil, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum allowed")
	client.AssertNotCalled(t, "OpenChannel", mock.Anything)
}

func TestOpenChannel_ClientErrorPropagates(t *testing.T) {
	cfg := baseTestConfig()
	client := new(MockLightningClient)
	client.On("OpenChannel", mock.Anything).Return(nil, assert.AnError)
	i := newTestInterceptor(cfg, client, nil, nil)

	_, err := i.openChannel([]byte{1}, []byte{2}, 20_000_000, 20_000_000, nil, false)
	assert.ErrorIs(t, err, assert.AnError)
}

func TestOpenChannel_FeeEstimatorErrorFallsBackToTargetConf(t *testing.T) {
	cfg := baseTestConfig()
	client := new(MockLightningClient)
	expectedOutpoint := newTestOutPoint()

	client.On("OpenChannel", mock.MatchedBy(func(req *lightning.OpenChannelRequest) bool {
		return req.TargetConf != nil && *req.TargetConf == cfg.TargetConf
	})).Return(expectedOutpoint, nil)

	failingFee := &fakeFeeEstimator{err: assert.AnError}
	store := new(MockStore)
	store.On("SetFundingTx", mock.Anything, expectedOutpoint).Return(nil)
	i := newTestInterceptor(cfg, client, failingFee, store)

	_, err := i.openChannel([]byte{1}, []byte{2}, 20_000_000, 20_000_000, nil, false)
	require.NoError(t, err, "a fee-estimator failure must fall back to configured TargetConf, not abort the open")
	client.AssertExpectations(t)
}

func TestOpenChannel_CallsSetFundingTxOnSuccess(t *testing.T) {
	cfg := baseTestConfig()
	client := new(MockLightningClient)
	expectedOutpoint := newTestOutPoint()
	client.On("OpenChannel", mock.Anything).Return(expectedOutpoint, nil)

	store := new(MockStore)
	store.On("SetFundingTx", mock.Anything, expectedOutpoint).Return(nil)
	i := newTestInterceptor(cfg, client, nil, store)

	outpoint, err := i.openChannel([]byte{1}, []byte{2}, 20_000_000, 20_000_000, nil, false)
	require.NoError(t, err)
	assert.Equal(t, expectedOutpoint, outpoint)
}

// TestOpenChannel_CapacityLeavesRoomForChannelReserve reproduces the exact
// live scenario that produced a permanent FAILURE_REASON_NO_ROUTE
// (root-caused 2026-07-16, see BACKLOG.md): incomingAmountMloki/1000 +
// AdditionalChannelCapacity alone (5_000_000 + 21_000 = 5_021_000 loki)
// nominally exceeds the outgoing amount (4_980_079 loki after flspd's own
// JIT fee), but flnd only ever advertises ~99% of a channel's capacity as
// forwardable (BOLT2's mandatory reserve) -- 5_021_000*0.99 = 4_970_790,
// which is *less* than what needs to be forwarded. openChannel must size
// capacity so the post-reserve forwardable amount actually covers
// outgoingAmountMloki, not just nominal capacity.
func TestOpenChannel_CapacityLeavesRoomForChannelReserve(t *testing.T) {
	cfg := baseTestConfig()
	cfg.JitEnabled = true
	cfg.AdditionalChannelCapacity = 21_000
	client := new(MockLightningClient)
	expectedOutpoint := newTestOutPoint()

	const incomingAmountMloki = 5_000_000_000
	const outgoingAmountMloki = 4_980_079_681
	const outgoingLoki = outgoingAmountMloki / 1000

	var gotCapacity uint64
	client.On("OpenChannel", mock.MatchedBy(func(req *lightning.OpenChannelRequest) bool {
		gotCapacity = req.CapacityLoki
		return true
	})).Return(expectedOutpoint, nil)

	store := new(MockStore)
	store.On("SetFundingTx", mock.Anything, expectedOutpoint).Return(nil)
	i := newTestInterceptor(cfg, client, nil, store)

	_, err := i.openChannel([]byte{1}, []byte{2}, incomingAmountMloki, outgoingAmountMloki, nil, true)
	require.NoError(t, err)

	forwardable := gotCapacity - gotCapacity/100 // flnd's default 1% reserve
	assert.GreaterOrEqualf(t, forwardable, uint64(outgoingLoki),
		"capacity %d leaves only %d forwardable after the 1%% reserve, short of the %d that must be forwarded",
		gotCapacity, forwardable, outgoingLoki)
}

func TestNewInterceptHandler_DecodesNodePubkeyIntoNodeid(t *testing.T) {
	cfg := config.NewNodeConfigRef(&config.NodeConfig{NodePubkey: "deadbeef"})
	store := new(MockStore)

	i := NewInterceptHandler(nil, cfg, store, nil, nil, nil, chain.FeeStrategyHalfHour, nil, nil)

	require.NotNil(t, i)
	assert.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, i.nodeid)
}
