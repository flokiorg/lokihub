package dashboard

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/history"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Mock definitions
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
	return m.Called(reg).Error(0)
}

func (m *MockOpeningStore) GetLsps2BuyRegistration(scid uint64) (*core.Lsps2BuyRegistration, error) {
	args := m.Called(scid)
	return args.Get(0).(*core.Lsps2BuyRegistration), args.Error(1)
}

func (m *MockOpeningStore) RegisterLsps1Order(order *core.Lsps1Order) error {
	return m.Called(order).Error(0)
}

func (m *MockOpeningStore) GetLsps1Order(orderId string) (*core.Lsps1Order, error) {
	args := m.Called(orderId)
	return args.Get(0).(*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningStore) GetLsps1OrderByPaymentInvoice(paymentInvoice string) (*core.Lsps1Order, error) {
	args := m.Called(paymentInvoice)
	return args.Get(0).(*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningStore) GetPaidLsps1Order(clientNodeId string) (*core.Lsps1Order, error) {
	args := m.Called(clientNodeId)
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

func (m *MockOpeningStore) UpdateLsps1OrderState(orderId string, newState string, newPaymentState string, expectedCurrentState string) (bool, error) {
	args := m.Called(orderId, newState, newPaymentState, expectedCurrentState)
	return args.Bool(0), args.Error(1)
}

func (m *MockOpeningStore) UpdateLsps1OrderFunding(orderId string, fundingTxid string, fundingOutpoint int) (bool, error) {
	args := m.Called(orderId, fundingTxid, fundingOutpoint)
	return args.Bool(0), args.Error(1)
}

func (m *MockOpeningStore) RegisterLsps5Webhook(webhook *core.Lsps5Webhook) error {
	return m.Called(webhook).Error(0)
}

func (m *MockOpeningStore) GetLsps5Webhooks(clientNodeId string) ([]*core.Lsps5Webhook, error) {
	args := m.Called(clientNodeId)
	return args.Get(0).([]*core.Lsps5Webhook), args.Error(1)
}

func (m *MockOpeningStore) RemoveLsps5Webhook(clientNodeId, appName string) error {
	return m.Called(clientNodeId, appName).Error(0)
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

func (m *MockOpeningStore) ListLsps1Orders(limit, offset int) ([]*core.Lsps1Order, error) {
	args := m.Called(limit, offset)
	return args.Get(0).([]*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningStore) GetAllCreatedLsps1Orders() ([]*core.Lsps1Order, error) {
	args := m.Called()
	return args.Get(0).([]*core.Lsps1Order), args.Error(1)
}

type MockOpeningService struct {
	mock.Mock
}

func (m *MockOpeningService) GetFeeParamsMenu(token string, privateKey *btcec.PrivateKey) ([]*core.OpeningFeeParams, error) {
	args := m.Called(token, privateKey)
	return args.Get(0).([]*core.OpeningFeeParams), args.Error(1)
}

func (m *MockOpeningService) ValidateOpeningFeeParams(params *core.OpeningFeeParams, publicKey *btcec.PublicKey) bool {
	return m.Called(params, publicKey).Bool(0)
}

func (m *MockOpeningService) IsCurrentChainFeeCheaper(currentFeeratePerVByte *float64, referenceFeeratePerVByte float64) bool {
	return m.Called(currentFeeratePerVByte, referenceFeeratePerVByte).Bool(0)
}

func (m *MockOpeningService) GetLsps1Order(orderId string) (*core.Lsps1Order, error) {
	args := m.Called(orderId)
	return args.Get(0).(*core.Lsps1Order), args.Error(1)
}

func (m *MockOpeningService) RegisterLsps5Webhook(webhook *core.Lsps5Webhook) error {
	return m.Called(webhook).Error(0)
}

func (m *MockOpeningService) GetLsps5Webhooks(clientNodeId string) ([]*core.Lsps5Webhook, error) {
	args := m.Called(clientNodeId)
	return args.Get(0).([]*core.Lsps5Webhook), args.Error(1)
}

func (m *MockOpeningService) RemoveLsps5Webhook(clientNodeId, appName string) error {
	return m.Called(clientNodeId, appName).Error(0)
}

func (m *MockOpeningService) CountLsps1Orders(clientNodeId string, state string, excludeExpiredBefore *time.Time) (int, error) {
	return m.Called(clientNodeId, state, excludeExpiredBefore).Int(0), m.Called(clientNodeId, state, excludeExpiredBefore).Error(1)
}

func (m *MockOpeningService) CountLsps2PendingRegistrations(peerId string) (int, error) {
	args := m.Called(peerId)
	return args.Int(0), args.Error(1)
}

func (m *MockOpeningService) CountLsps5Webhooks(clientNodeId string) (int, error) {
	return m.Called(clientNodeId).Int(0), m.Called(clientNodeId).Error(1)
}

type MockHistoryStore struct {
	mock.Mock
}

func (m *MockHistoryStore) UpdateChannels(ctx context.Context, updates []*history.ChannelUpdate) error {
	return m.Called(ctx, updates).Error(0)
}

func (m *MockHistoryStore) InsertForwards(ctx context.Context, forwards []*history.Forward, nodeId []byte) error {
	return m.Called(ctx, forwards, nodeId).Error(0)
}

func (m *MockHistoryStore) UpdateForwards(ctx context.Context, forwards []*history.Forward, nodeId []byte) error {
	return m.Called(ctx, forwards, nodeId).Error(0)
}

func (m *MockHistoryStore) FetchLndForwardOffset(ctx context.Context, nodeId []byte) (*time.Time, error) {
	args := m.Called(ctx, nodeId)
	return args.Get(0).(*time.Time), args.Error(1)
}

func (m *MockHistoryStore) AddOpenChannelHtlc(ctx context.Context, htlc *history.OpenChannelHtlc) error {
	return m.Called(ctx, htlc).Error(0)
}

type MockLightningClient struct {
	mock.Mock
}

func (m *MockLightningClient) GetInfo() (*lightning.GetInfoResult, error) {
	args := m.Called()
	return args.Get(0).(*lightning.GetInfoResult), args.Error(1)
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
	return args.Get(0).(*lightning.GetChannelResult), args.Error(1)
}

func (m *MockLightningClient) WaitChannelPointActive(peerID []byte, channelPoint wire.OutPoint, deadline time.Time) (*lightning.GetChannelResult, error) {
	args := m.Called(peerID, channelPoint, deadline)
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
	return m.Called(peerID, deadline).Error(0)
}

func (m *MockLightningClient) WaitChannelActive(peerID []byte, deadline time.Time) error {
	return m.Called(peerID, deadline).Error(0)
}

func (m *MockLightningClient) ListChannels() ([]*lightning.Channel, error) {
	args := m.Called()
	return args.Get(0).([]*lightning.Channel), args.Error(1)
}

func (m *MockLightningClient) SubscribeInvoices() (<-chan *lightning.InvoiceUpdate, <-chan error, error) {
	args := m.Called()
	return args.Get(0).(<-chan *lightning.InvoiceUpdate), args.Get(1).(<-chan error), args.Error(2)
}

func (m *MockLightningClient) WalletBalance(ctx context.Context) (*lightning.WalletBalanceResult, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*lightning.WalletBalanceResult), args.Error(1)
}

func (m *MockLightningClient) GetBlockHeight() (uint32, error) {
	args := m.Called()
	return args.Get(0).(uint32), args.Error(1)
}

func (m *MockLightningClient) QueryRoutes(destination []byte, amountMloki uint64, outgoingChanId uint64) ([]*lightning.Route, error) {
	args := m.Called(destination, amountMloki, outgoingChanId)
	return args.Get(0).([]*lightning.Route), args.Error(1)
}

func (m *MockLightningClient) IsChannelRoutable(scid uint64) (bool, error) {
	args := m.Called(scid)
	return args.Bool(0), args.Error(1)
}

func (m *MockLightningClient) GetNodeInfo(pubkey string) (*lightning.NodeInfo, error) {
	args := m.Called(pubkey)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*lightning.NodeInfo), args.Error(1)
}

func (m *MockLightningClient) SubscribePeerOnline(ctx context.Context) (<-chan string, error) {
	args := m.Called(ctx)
	return args.Get(0).(<-chan string), args.Error(1)
}

func (m *MockLightningClient) AddInvoice(ctx context.Context, invoice *lnrpc.Invoice) (*lnrpc.AddInvoiceResponse, error) {
	args := m.Called(ctx, invoice)
	return args.Get(0).(*lnrpc.AddInvoiceResponse), args.Error(1)
}

func (m *MockLightningClient) LookupInvoiceState(ctx context.Context, paymentRequest string) (lightning.InvoiceState, error) {
	args := m.Called(ctx, paymentRequest)
	return args.Get(0).(lightning.InvoiceState), args.Error(1)
}

func (m *MockLightningClient) LookupInvoiceStateByHash(ctx context.Context, paymentHash []byte) (lightning.InvoiceState, error) {
	args := m.Called(ctx, paymentHash)
	return args.Get(0).(lightning.InvoiceState), args.Error(1)
}

func (m *MockLightningClient) SubscribeSingleInvoice(ctx context.Context, paymentHash []byte) (<-chan *lightning.InvoiceUpdate, <-chan error, error) {
	args := m.Called(ctx, paymentHash)
	return args.Get(0).(<-chan *lightning.InvoiceUpdate), args.Get(1).(<-chan error), args.Error(2)
}

func (m *MockLightningClient) SettleInvoice(ctx context.Context, preimage []byte) error {
	args := m.Called(ctx, preimage)
	return args.Error(0)
}

func (m *MockLightningClient) CancelInvoice(ctx context.Context, paymentHash []byte) error {
	args := m.Called(ctx, paymentHash)
	return args.Error(0)
}

func TestDashboardService_RPC(t *testing.T) {
	mockNodes := new(MockNodesService)
	mockOpeningSvc := new(MockOpeningService)
	mockStore := new(MockOpeningStore)
	mockHistory := new(MockHistoryStore)
	mockLnd := new(MockLightningClient)

	// Setup a test node
	testNode := &core.Node{
		NodeId: []byte("testnode"),
		Client: mockLnd,
		NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
			Name:       "Test Node",
			NodePubkey: "02abc...",
		}),
	}

	mockNodes.On("GetNodes").Return([]*core.Node{testNode})
	mockStore.On("GetReservedLiquidity").Return(uint64(10000), nil)

	s := NewHandlers(mockNodes, mockOpeningSvc, mockStore, mockHistory, "/tmp/nodes.test.yaml")

	t.Run("get_summary", func(t *testing.T) {
		// Scoped with .Once() so these don't leak into later subtests that
		// register their own, more specific expectations for the same
		// mocked methods (list_peers, list_orders below).
		mockLnd.On("WalletBalance", mock.Anything).Return(&lightning.WalletBalanceResult{
			ConfirmedBalance: 1000000,
		}, nil).Once()
		// Matches the specific (summaryListLimit, 0) args handleGetSummary
		// and handleListPeers both call with — not scoped with .Once()
		// since handleListPeers (next subtest) calls it too. Distinct from
		// list_orders' own (defaultListLimit, 0) expectation below, since
		// summaryListLimit != defaultListLimit.
		mockStore.On("ListLsps1Orders", 1000, 0).Return([]*core.Lsps1Order{}, nil)
		mockLnd.On("GetInfo").Return(&lightning.GetInfoResult{
			SyncedToChain:       true,
			BlockHeight:         500,
			NumPeers:            3,
			NumActiveChannels:   1,
			NumPendingChannels:  0,
			NumInactiveChannels: 1,
		}, nil).Once()
		// One active channel (counted into outbound/inbound liquidity) and
		// one inactive channel with a much larger balance (must be
		// excluded), regression-testing the Active-only aggregation filter.
		mockLnd.On("ListChannels").Return([]*lightning.Channel{
			{Active: true, LocalBalance: 500000, RemoteBalance: 250000},
			{Active: false, LocalBalance: 9000000, RemoteBalance: 9000000},
		}, nil).Once()

		req := &lsps.JsonRpcRequest{
			Id:     json.RawMessage(`1`),
			Method: "get_summary",
		}
		resp := s.handleGetSummary(req)
		assert.Nil(t, resp.Error)

		result := resp.Result.(SummaryResponse)
		assert.Equal(t, "Test Node", result.NodeName)
		assert.Equal(t, uint64(10000), result.LiquidityBlocked)
		assert.Equal(t, uint64(500000), result.LiquidityOutbound)
		assert.Equal(t, uint64(250000), result.LiquidityInbound)
		assert.Equal(t, "online", result.NodeStatus)
		assert.True(t, result.SyncedToChain)
		assert.Equal(t, uint32(500), result.SyncHeight)
		assert.Equal(t, uint32(3), result.PeersConnected)
		assert.Equal(t, uint32(1), result.ChannelsActive)
		assert.Equal(t, uint32(0), result.ChannelsPending)
	})

	t.Run("list_peers", func(t *testing.T) {
		mockLnd.On("ListPeers").Return([]*lightning.Peer{
			{Pubkey: "peer1", Active: true},
		}, nil)

		req := &lsps.JsonRpcRequest{
			Id:     json.RawMessage(`2`),
			Method: "list_peers",
		}
		resp := s.handleListPeers(req)
		assert.Nil(t, resp.Error)

		peers := resp.Result.([]interface{})
		assert.Equal(t, 1, len(peers))
	})

	t.Run("list_orders", func(t *testing.T) {
		mockStore.On("ListLsps1Orders", 100, 0).Return([]*core.Lsps1Order{
			{OrderId: "order1", OrderState: "CREATED"},
		}, nil)
		// order1's PeerId ("") doesn't match any peer from ListPeers, so
		// handleListOrders falls back to GetNodeInfo to enrich it.
		mockLnd.On("GetNodeInfo", "").Return(nil, fmt.Errorf("not found"))

		req := &lsps.JsonRpcRequest{
			Id:     json.RawMessage(`3`),
			Method: "list_orders",
		}
		resp := s.handleListOrders(req)
		assert.Nil(t, resp.Error)

		orders := resp.Result.([]*core.Lsps1Order)
		assert.Equal(t, 1, len(orders))
	})

	t.Run("get_config", func(t *testing.T) {
		// Mock YAML file for config
		configPath := "/tmp/nodes.test.yaml"
		err := os.WriteFile(configPath, []byte("nodes:\n  - name: Test Node\n"), 0644)
		assert.NoError(t, err)
		defer func() { _ = os.Remove(configPath) }()

		req := &lsps.JsonRpcRequest{
			Id:     json.RawMessage(`4`),
			Method: "get_config",
		}
		resp := s.handleGetConfig(req)
		assert.Nil(t, resp.Error)
	})
}

// TestHandleGetConfig_StripsSecrets is the regression test for C-1:
// get_config used to return the full NodeConfig, including
// flspdPrivateKey, tokens (client auth tokens), and the flnd block
// (macaroon + TLS cert), to any authenticated dashboard client.
func TestHandleGetConfig_StripsSecrets(t *testing.T) {
	mockNodes := new(MockNodesService)
	node := &core.Node{
		NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
			Name:            "Secret Node",
			NodePubkey:      "02abc...",
			FlspdPrivateKey: config.InlineSecret("deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"),
			Tokens:          []string{"super-secret-client-token"},
			Lnd: &config.LndConfig{
				Address:  "flnd:10005",
				Macaroon: config.InlineSecret("0102030405"),
				Cert:     config.InlineSecret("-----BEGIN CERTIFICATE-----"),
			},
		}),
	}
	mockNodes.On("GetNodes").Return([]*core.Node{node})

	s := NewHandlers(mockNodes, nil, nil, nil, "")

	req := &lsps.JsonRpcRequest{Id: json.RawMessage(`1`), Method: "get_config"}
	resp := s.handleGetConfig(req)
	assert.Nil(t, resp.Error)

	// Round-trip through JSON the same way a real RPC response would, so
	// this test catches the bug even if a future change returns the
	// secrets under a different Go field but the same JSON shape.
	raw, err := json.Marshal(resp.Result)
	assert.NoError(t, err)
	assert.NotContains(t, string(raw), "deadbeefdeadbeef", "flspdPrivateKey leaked into the response")
	assert.NotContains(t, string(raw), "super-secret-client-token", "tokens leaked into the response")
	assert.NotContains(t, string(raw), "0102030405", "macaroon leaked into the response")
	assert.NotContains(t, string(raw), "BEGIN CERTIFICATE", "TLS cert leaked into the response")

	// And the safe fields should still be present — this isn't just a
	// bug fix, it still needs to be useful.
	assert.Contains(t, string(raw), "Secret Node")
	assert.Contains(t, string(raw), "02abc")
}

// TestHandleUpdateConfig_PreservesOtherNodes is the regression test for
// C-2: update_config used to marshal only the single edited node back to
// nodes.yaml, silently dropping every other configured node on a
// multi-node deployment.
func twoNodeFleet() (*MockNodesService, *core.Node, *core.Node) {
	mockNodes := new(MockNodesService)
	nodeA := &core.Node{
		NodeId: append([]byte{0x02}, bytes.Repeat([]byte{0xaa}, 32)...),
		NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
			Name:                   "Node A",
			NodePubkey:             "02aaa...",
			TargetConf:             6,
			ChannelMinimumFeeMloki: 1000,
		}),
	}
	nodeB := &core.Node{
		NodeId: append([]byte{0x03}, bytes.Repeat([]byte{0xbb}, 32)...),
		NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
			Name:                   "Node B",
			NodePubkey:             "03bbb...",
			TargetConf:             6,
			ChannelMinimumFeeMloki: 1000,
		}),
	}
	mockNodes.On("GetNodes").Return([]*core.Node{nodeA, nodeB})
	return mockNodes, nodeA, nodeB
}

func updateConfigReq(t *testing.T, node, name string) *lsps.JsonRpcRequest {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"node": node,
		"config": config.NodeConfig{
			Name:                   name,
			TargetConf:             6,
			ChannelMinimumFeeMloki: 1000,
		},
	})
	require.NoError(t, err)
	return &lsps.JsonRpcRequest{Id: json.RawMessage(`1`), Method: "update_config", Params: body}
}

// C-2 with the node selector: editing the selected node keeps the others,
// in the file and in memory.
func TestHandleUpdateConfig_EditsOnlyTheSelectedNode(t *testing.T) {
	for _, target := range []string{"A", "B"} {
		t.Run(target, func(t *testing.T) {
			mockNodes, nodeA, nodeB := twoNodeFleet()
			configPath := filepath.Join(t.TempDir(), "nodes.yaml")
			s := NewHandlers(mockNodes, nil, nil, nil, configPath)

			edited, other := nodeA, nodeB
			if target == "B" {
				edited, other = nodeB, nodeA
			}
			otherName := other.NodeConfig.Load().Name
			resp := s.handleUpdateConfig(updateConfigReq(t, hex.EncodeToString(edited.NodeId), "Renamed"))
			require.Nil(t, resp.Error)

			data, err := os.ReadFile(configPath)
			require.NoError(t, err)
			var persisted []*config.NodeConfig
			require.NoError(t, yaml.Unmarshal(data, &persisted))
			require.Len(t, persisted, 2, "both nodes should still be present in the persisted file")
			names := []string{persisted[0].Name, persisted[1].Name}
			assert.Contains(t, names, "Renamed")
			assert.Contains(t, names, otherName)
			assert.Equal(t, "Renamed", edited.NodeConfig.Load().Name)
			assert.Equal(t, otherName, other.NodeConfig.Load().Name, "the other node is untouched")
		})
	}
}

// T8-6 (R4-F2): saves for two nodes at once each rebuilt the file from the
// in-memory list before the other's Store, so the file kept only one edit
// while memory held both, and both wrote the same .tmp path.
func TestHandleUpdateConfig_ConcurrentSavesKeepBothNodes(t *testing.T) {
	mockNodes, nodeA, nodeB := twoNodeFleet()
	configPath := filepath.Join(t.TempDir(), "nodes.yaml")
	s := NewHandlers(mockNodes, nil, nil, nil, configPath)
	idA, idB := hex.EncodeToString(nodeA.NodeId), hex.EncodeToString(nodeB.NodeId)

	for round := range 50 {
		nameA, nameB := fmt.Sprintf("A-%d", round), fmt.Sprintf("B-%d", round)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, req := range []*lsps.JsonRpcRequest{updateConfigReq(t, idA, nameA), updateConfigReq(t, idB, nameB)} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				resp := s.handleUpdateConfig(req)
				assert.Nil(t, resp.Error)
			}()
		}
		close(start)
		wg.Wait()

		data, err := os.ReadFile(configPath)
		require.NoError(t, err)
		var persisted []*config.NodeConfig
		require.NoError(t, yaml.Unmarshal(data, &persisted), "round %d: the file is valid YAML", round)
		require.Len(t, persisted, 2)
		require.Equal(t, []string{nameA, nameB}, []string{persisted[0].Name, persisted[1].Name}, "round %d: the file holds both saves", round)
		require.Equal(t, nameA, nodeA.NodeConfig.Load().Name)
		require.Equal(t, nameB, nodeB.NodeConfig.Load().Name)
	}
	matches, err := filepath.Glob(configPath + "*.tmp")
	require.NoError(t, err)
	assert.Empty(t, matches, "no temporary file is left behind")
}

func TestHandleUpdateConfig_FleetNeedsAKnownNode(t *testing.T) {
	mockNodes, nodeA, nodeB := twoNodeFleet()
	configPath := filepath.Join(t.TempDir(), "nodes.yaml")
	s := NewHandlers(mockNodes, nil, nil, nil, configPath)

	for name, node := range map[string]string{"no selector": "", "unknown node": "02" + strings.Repeat("cc", 32)} {
		t.Run(name, func(t *testing.T) {
			resp := s.handleUpdateConfig(updateConfigReq(t, node, "Renamed"))
			require.NotNil(t, resp.Error)
			assert.Equal(t, -32602, resp.Error.Code)
			assert.Equal(t, "Node A", nodeA.NodeConfig.Load().Name)
			assert.Equal(t, "Node B", nodeB.NodeConfig.Load().Name)
			_, err := os.Stat(configPath)
			assert.True(t, os.IsNotExist(err), "nothing persisted")
		})
	}
}

func TestHandleUpdateConfig_SingleNodeNeedsNoSelector(t *testing.T) {
	mockNodes := new(MockNodesService)
	only := &core.Node{NodeId: []byte{0x02, 0x01}, NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{Name: "Only", TargetConf: 6, ChannelMinimumFeeMloki: 1000})}
	mockNodes.On("GetNodes").Return([]*core.Node{only})
	s := NewHandlers(mockNodes, nil, nil, nil, "")

	resp := s.handleUpdateConfig(updateConfigReq(t, "", "Renamed"))
	require.Nil(t, resp.Error)
	assert.Equal(t, "Renamed", only.NodeConfig.Load().Name)

	resp = s.handleGetConfig(&lsps.JsonRpcRequest{Id: json.RawMessage(`2`), Method: "get_config"})
	require.Nil(t, resp.Error)
}

func TestHandleListNodes_ListsTheFleet(t *testing.T) {
	mockNodes, nodeA, nodeB := twoNodeFleet()
	s := NewHandlers(mockNodes, nil, nil, nil, "")
	resp := s.Route(&lsps.JsonRpcRequest{Id: json.RawMessage(`1`), Method: "list_nodes"})
	require.Nil(t, resp.Error)
	nodes := resp.Result.(map[string]any)["nodes"].([]NodeView)
	assert.Equal(t, []NodeView{
		{NodeID: hex.EncodeToString(nodeA.NodeId), Name: "Node A"},
		{NodeID: hex.EncodeToString(nodeB.NodeId), Name: "Node B"},
	}, nodes)
}

// TestNodeConfig_ConcurrentReadWriteNoRace is the regression test for C-3:
// config used to be a raw *config.NodeConfig mutated in place
// (*nodes[0].NodeConfig = newValue) with no synchronization against
// concurrent readers on the HTLC hot path. Run with -race: this must pass
// clean, not just avoid a panic.
func TestNodeConfig_ConcurrentReadWriteNoRace(t *testing.T) {
	ref := config.NewNodeConfigRef(&config.NodeConfig{TimeLockDelta: 144})

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Simulate the interceptor's hot path: many concurrent readers taking
	// a snapshot and reading fields off it.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					cfg := ref.Load()
					_ = cfg.TimeLockDelta
					_ = cfg.AdditionalChannelCapacity
				}
			}
		}()
	}

	// Simulate the dashboard: one writer swapping in a whole new config
	// repeatedly.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			ref.Store(&config.NodeConfig{TimeLockDelta: uint32(i)})
		}
	}()

	// Let the readers run a little past the writer finishing, then stop.
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func announceTestNode() *core.Node {
	return &core.Node{
		NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{
			Name:       "Node A",
			NodePubkey: "02aaa...",
			TargetConf: 6,
			Lnd:        &config.LndConfig{Address: "flnd:10005", Macaroon: config.InlineSecret("0102")},
		}),
	}
}

func updateConfigRequest(t *testing.T, cfg config.NodeConfig) *lsps.JsonRpcRequest {
	t.Helper()
	body, err := json.Marshal(struct {
		Config config.NodeConfig `json:"config"`
	}{cfg})
	assert.NoError(t, err)
	return &lsps.JsonRpcRequest{Id: json.RawMessage(`1`), Method: "update_config", Params: body}
}

// TestHandleUpdateConfig_AnnounceChecks covers T8-2's hot-swap path: the
// announce block is validated, and turning announcing on also runs the
// announce check against the node's preserved flnd block.
func TestHandleUpdateConfig_AnnounceChecks(t *testing.T) {
	node := announceTestNode()
	mockNodes := new(MockNodesService)
	mockNodes.On("GetNodes").Return([]*core.Node{node})
	s := NewHandlers(mockNodes, nil, nil, nil, "")

	var checked *config.NodeConfig
	s.SetAnnounce([]string{"wss://relay.example.com"}, func(cfg *config.NodeConfig) error {
		checked = cfg
		return fmt.Errorf("flnd.macaroon lacks signer:generate")
	})

	// No addr while enabled: refused by Validate, before the check.
	resp := s.handleUpdateConfig(updateConfigRequest(t, config.NodeConfig{
		Name: "Node A", TargetConf: 6,
		Announce: &config.AnnounceConfig{Enabled: true},
	}))
	assert.NotNil(t, resp.Error)
	assert.Nil(t, checked)

	// Valid block, failing check: refused, and the check saw the preserved flnd block.
	resp = s.handleUpdateConfig(updateConfigRequest(t, config.NodeConfig{
		Name: "Node A", TargetConf: 6,
		Announce: &config.AnnounceConfig{Enabled: true, Addrs: []string{"203.0.113.7:9735"}},
	}))
	assert.NotNil(t, resp.Error)
	if assert.NotNil(t, checked) {
		assert.Equal(t, "flnd:10005", checked.Lnd.Address)
	}
	assert.Nil(t, node.NodeConfig.Load().Announce, "refused update must not be applied")

	// Announcing off: the check is not run, update applies.
	checked = nil
	resp = s.handleUpdateConfig(updateConfigRequest(t, config.NodeConfig{
		Name: "Node A", TargetConf: 6,
		Announce: &config.AnnounceConfig{Enabled: false, Title: "Draft"},
	}))
	assert.Nil(t, resp.Error)
	assert.Nil(t, checked)
	assert.Equal(t, "Draft", node.NodeConfig.Load().Announce.Title)

	// Check passes: update applies with the addrs exactly as entered.
	s.SetAnnounce([]string{"wss://relay.example.com"}, func(*config.NodeConfig) error { return nil })
	addrs := []string{"[2001:db8::1]:9735", "203.0.113.7:9735"}
	resp = s.handleUpdateConfig(updateConfigRequest(t, config.NodeConfig{
		Name: "Node A", TargetConf: 6,
		Announce: &config.AnnounceConfig{Enabled: true, Addrs: addrs},
	}))
	assert.Nil(t, resp.Error)
	assert.Equal(t, addrs, node.NodeConfig.Load().Announce.Addrs)
}

// TestStripConfigSecrets_AllowlistDropsUnknownField is the FLSPD-0017 /
// R-LSP-CFGLEAK regression: get_config serializes only a known-safe
// allowlist, so a config key the allowlist doesn't know about -- e.g. a
// secret added by a later change -- is dropped by default instead of leaking,
// which the former hand-maintained denylist would have let through.
func TestStripConfigSecrets_AllowlistDropsUnknownField(t *testing.T) {
	in := map[string]interface{}{
		"name":              "n1",              // allowlisted safe field
		"host":              "example:9735",    // allowlisted safe field
		"futureSecretToken": "should-not-leak", // not in the allowlist
		"flspdPrivateKey":   "deadbeef",        // deliberately excluded secret
	}

	out := filterToAllowlist(in)

	assert.Contains(t, out, "name", "allowlisted field must survive")
	assert.Contains(t, out, "host", "allowlisted field must survive")
	assert.NotContains(t, out, "futureSecretToken", "a field outside the allowlist must not be serialized")
	assert.NotContains(t, out, "flspdPrivateKey", "excluded secret must not be serialized")
}

func TestHandleGetConfig_AnnounceRelays(t *testing.T) {
	mockNodes := new(MockNodesService)
	mockNodes.On("GetNodes").Return([]*core.Node{announceTestNode()})
	s := NewHandlers(mockNodes, nil, nil, nil, "")

	raw, err := json.Marshal(s.handleGetConfig(&lsps.JsonRpcRequest{Id: json.RawMessage(`1`), Method: "get_config"}).Result)
	assert.NoError(t, err)
	assert.Contains(t, string(raw), `"announce_relays":[]`)

	s.SetAnnounce([]string{"wss://relay.example.com"}, nil)
	raw, err = json.Marshal(s.handleGetConfig(&lsps.JsonRpcRequest{Id: json.RawMessage(`1`), Method: "get_config"}).Result)
	assert.NoError(t, err)
	assert.Contains(t, string(raw), `"announce_relays":["wss://relay.example.com"]`)
}
