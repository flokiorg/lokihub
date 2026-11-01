package interceptor

import (
	"context"
	"time"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/chain"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/stretchr/testify/mock"
)

// MockNotifier is a minimal testify mock of notifications.Notifier.
type MockNotifier struct {
	mock.Mock
}

func (m *MockNotifier) Notify(pubkey string, paymenthash string, nodePrivKey interface{}) (bool, error) {
	args := m.Called(pubkey, paymenthash, nodePrivKey)
	return args.Bool(0), args.Error(1)
}

func (m *MockNotifier) NotifyAsync(pubkey string, paymenthash string, nodePrivKey interface{}) {
	m.Called(pubkey, paymenthash, nodePrivKey)
}

func (m *MockNotifier) NotifyWebhookRegistered(pubkey string, webhook *core.Lsps5Webhook) bool {
	args := m.Called(pubkey, webhook)
	return args.Bool(0)
}

func (m *MockNotifier) NotifyOrderStateChanged(pubkey string, orderId string, state string, channelPoint string, errorMsg string) (bool, error) {
	args := m.Called(pubkey, orderId, state, channelPoint, errorMsg)
	return args.Bool(0), args.Error(1)
}

func (m *MockNotifier) NotifyExpirySoon(pubkey string, timeout int, nodePrivKey interface{}, paymentHash string) (bool, error) {
	args := m.Called(pubkey, timeout, nodePrivKey, paymentHash)
	return args.Bool(0), args.Error(1)
}

func (m *MockNotifier) NotifyLiquidityManagementRequest(pubkey string, nodePrivKey interface{}) (bool, error) {
	args := m.Called(pubkey, nodePrivKey)
	return args.Bool(0), args.Error(1)
}

func (m *MockNotifier) NotifyOnionMessageIncoming(pubkey string, nodePrivKey interface{}) (bool, error) {
	args := m.Called(pubkey, nodePrivKey)
	return args.Bool(0), args.Error(1)
}

// MockLightningClient is a minimal testify mock of the full lightning.Client
// port, used to unit-test Interceptor methods (like openChannel) without a
// real flnd node. Only the methods a given test actually exercises need
// .On(...) expectations set; the rest simply aren't called.
type MockLightningClient struct {
	mock.Mock
}

func (m *MockLightningClient) GetInfo() (*lightning.GetInfoResult, error) {
	args := m.Called()
	res, _ := args.Get(0).(*lightning.GetInfoResult)
	return res, args.Error(1)
}

func (m *MockLightningClient) IsConnected(destination []byte) (bool, error) {
	args := m.Called(destination)
	return args.Bool(0), args.Error(1)
}

func (m *MockLightningClient) OpenChannel(req *lightning.OpenChannelRequest) (*wire.OutPoint, error) {
	args := m.Called(req)
	res, _ := args.Get(0).(*wire.OutPoint)
	return res, args.Error(1)
}

func (m *MockLightningClient) UpdateChannelPolicy(channelPoint wire.OutPoint, policy *lightning.RoutingPolicy) error {
	args := m.Called(channelPoint, policy)
	return args.Error(0)
}

func (m *MockLightningClient) GetChannel(peerID []byte, channelPoint wire.OutPoint) (*lightning.GetChannelResult, error) {
	args := m.Called(peerID, channelPoint)
	res, _ := args.Get(0).(*lightning.GetChannelResult)
	return res, args.Error(1)
}

func (m *MockLightningClient) WaitChannelPointActive(peerID []byte, channelPoint wire.OutPoint, deadline time.Time) (*lightning.GetChannelResult, error) {
	args := m.Called(peerID, channelPoint, deadline)
	res, _ := args.Get(0).(*lightning.GetChannelResult)
	return res, args.Error(1)
}

func (m *MockLightningClient) GetPeerId(scid *lightning.ShortChannelID) ([]byte, error) {
	args := m.Called(scid)
	res, _ := args.Get(0).([]byte)
	return res, args.Error(1)
}

func (m *MockLightningClient) GetClosedChannels(nodeID string, channelPoints map[string]uint64) (map[string]uint64, error) {
	args := m.Called(nodeID, channelPoints)
	res, _ := args.Get(0).(map[string]uint64)
	return res, args.Error(1)
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
	res, _ := args.Get(0).([]*lightning.Channel)
	return res, args.Error(1)
}

func (m *MockLightningClient) ListPeers() ([]*lightning.Peer, error) {
	args := m.Called()
	res, _ := args.Get(0).([]*lightning.Peer)
	return res, args.Error(1)
}

func (m *MockLightningClient) SubscribeInvoices() (<-chan *lightning.InvoiceUpdate, <-chan error, error) {
	args := m.Called()
	updates, _ := args.Get(0).(<-chan *lightning.InvoiceUpdate)
	errs, _ := args.Get(1).(<-chan error)
	return updates, errs, args.Error(2)
}

func (m *MockLightningClient) LookupInvoiceState(ctx context.Context, paymentRequest string) (lightning.InvoiceState, error) {
	args := m.Called(ctx, paymentRequest)
	state, _ := args.Get(0).(lightning.InvoiceState)
	return state, args.Error(1)
}

func (m *MockLightningClient) LookupInvoiceStateByHash(ctx context.Context, paymentHash []byte) (lightning.InvoiceState, error) {
	args := m.Called(ctx, paymentHash)
	state, _ := args.Get(0).(lightning.InvoiceState)
	return state, args.Error(1)
}

func (m *MockLightningClient) SubscribeSingleInvoice(ctx context.Context, paymentHash []byte) (<-chan *lightning.InvoiceUpdate, <-chan error, error) {
	args := m.Called(ctx, paymentHash)
	updates, _ := args.Get(0).(<-chan *lightning.InvoiceUpdate)
	errs, _ := args.Get(1).(<-chan error)
	return updates, errs, args.Error(2)
}

func (m *MockLightningClient) SettleInvoice(ctx context.Context, preimage []byte) error {
	args := m.Called(ctx, preimage)
	return args.Error(0)
}

func (m *MockLightningClient) CancelInvoice(ctx context.Context, paymentHash []byte) error {
	args := m.Called(ctx, paymentHash)
	return args.Error(0)
}

func (m *MockLightningClient) WalletBalance(ctx context.Context) (*lightning.WalletBalanceResult, error) {
	args := m.Called(ctx)
	res, _ := args.Get(0).(*lightning.WalletBalanceResult)
	return res, args.Error(1)
}

func (m *MockLightningClient) GetBlockHeight() (uint32, error) {
	args := m.Called()
	height, _ := args.Get(0).(uint32)
	return height, args.Error(1)
}

func (m *MockLightningClient) QueryRoutes(destination []byte, amountMloki uint64, outgoingChanId uint64) ([]*lightning.Route, error) {
	args := m.Called(destination, amountMloki, outgoingChanId)
	res, _ := args.Get(0).([]*lightning.Route)
	return res, args.Error(1)
}

func (m *MockLightningClient) IsChannelRoutable(scid uint64) (bool, error) {
	args := m.Called(scid)
	return args.Bool(0), args.Error(1)
}

func (m *MockLightningClient) IsChannelPolicyApplied(scid uint64, peerPubkey []byte, timeLockDelta uint32) (bool, error) {
	args := m.Called(scid, peerPubkey, timeLockDelta)
	return args.Bool(0), args.Error(1)
}

func (m *MockLightningClient) GetNodeInfo(pubkey string) (*lightning.NodeInfo, error) {
	args := m.Called(pubkey)
	res, _ := args.Get(0).(*lightning.NodeInfo)
	return res, args.Error(1)
}

// fakeFeeEstimator is a trivial chain.FeeEstimator that returns a fixed
// estimation (or a configured error), avoiding a full mock for interceptor
// tests that don't care about fee-strategy selection itself.
type fakeFeeEstimator struct {
	estimation *chain.FeeEstimation
	err        error
}

func (f *fakeFeeEstimator) EstimateFeeRate(ctx context.Context, strategy chain.FeeStrategy) (*chain.FeeEstimation, error) {
	return f.estimation, f.err
}

func fixedFeeEstimation() *chain.FeeEstimation {
	rate := 1.0
	conf := uint32(6)
	return &chain.FeeEstimation{LokiPerVByte: &rate, TargetConf: &conf}
}
