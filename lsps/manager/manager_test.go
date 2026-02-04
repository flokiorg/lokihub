package manager

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/flokiorg/lokihub/lsps/transport"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Mock LNClient for Manager tests
type mockLNClient struct {
	acceptorChan      chan lnclient.ChannelAcceptRequest
	respondFunc       func(id string, accept bool, zeroConf bool) error
	subscribeErr      error
	subscribeCalled   bool
	subscribeCallback func() // optional hook to check calls // Fixed typo 'checking calls'
}

func (m *mockLNClient) SubscribeChannelAcceptor(ctx context.Context) (<-chan lnclient.ChannelAcceptRequest, func(id string, accept bool, zeroConf bool) error, error) {
	m.subscribeCalled = true
	if m.subscribeCallback != nil {
		m.subscribeCallback()
	}
	if m.subscribeErr != nil {
		return nil, nil, m.subscribeErr
	}
	return m.acceptorChan, m.respondFunc, nil
}

// ... Implement other interface methods as no-ops or panics if not used ...
func (m *mockLNClient) SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error {
	return nil
}
func (m *mockLNClient) SubscribeCustomMessages(ctx context.Context) (<-chan lnclient.CustomMessage, <-chan error, error) {
	return nil, nil, nil
}

// Stub out required methods to satisfy interface
func (m *mockLNClient) SendPaymentSync(payReq string, amount *uint64) (*lnclient.PayInvoiceResponse, error) {
	return nil, nil
}
func (m *mockLNClient) SendKeysend(amount uint64, destination string, customRecords []lnclient.TLVRecord, preimage string) (*lnclient.PayKeysendResponse, error) {
	return nil, nil
}
func (m *mockLNClient) GetPubkey() string                                       { return "" }
func (m *mockLNClient) GetInfo(ctx context.Context) (*lnclient.NodeInfo, error) { return nil, nil }
func (m *mockLNClient) MakeInvoice(ctx context.Context, amount int64, description string, descriptionHash string, expiry int64, throughNodePubkey *string, lspJitChannelSCID *string, lspCltvExpiryDelta *uint16, lspFeeBaseMloki *uint64, lspFeeProportionalMillionths *uint32) (*lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClient) MakeHoldInvoice(ctx context.Context, amount int64, description string, descriptionHash string, expiry int64, paymentHash string) (*lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClient) SettleHoldInvoice(ctx context.Context, preimage string) error    { return nil }
func (m *mockLNClient) CancelHoldInvoice(ctx context.Context, paymentHash string) error { return nil }
func (m *mockLNClient) LookupInvoice(ctx context.Context, paymentHash string) (*lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClient) ListTransactions(ctx context.Context, from, until, limit, offset uint64, unpaid bool, invoiceType string) ([]lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClient) ListOnchainTransactions(ctx context.Context, from, until, limit, offset uint64) ([]lnclient.OnchainTransaction, error) {
	return nil, nil
}
func (m *mockLNClient) Shutdown() error                                              { return nil }
func (m *mockLNClient) ListChannels(ctx context.Context) ([]lnclient.Channel, error) { return nil, nil }
func (m *mockLNClient) GetNodeConnectionInfo(ctx context.Context) (*lnclient.NodeConnectionInfo, error) {
	return nil, nil
}
func (m *mockLNClient) GetNodeStatus(ctx context.Context) (*lnclient.NodeStatus, error) {
	return nil, nil
}
func (m *mockLNClient) ConnectPeer(ctx context.Context, connectPeerRequest *lnclient.ConnectPeerRequest) error {
	return nil
}
func (m *mockLNClient) OpenChannel(ctx context.Context, openChannelRequest *lnclient.OpenChannelRequest) (*lnclient.OpenChannelResponse, error) {
	return nil, nil
}
func (m *mockLNClient) CloseChannel(ctx context.Context, closeChannelRequest *lnclient.CloseChannelRequest) (*lnclient.CloseChannelResponse, error) {
	return nil, nil
}
func (m *mockLNClient) UpdateChannel(ctx context.Context, updateChannelRequest *lnclient.UpdateChannelRequest) error {
	return nil
}
func (m *mockLNClient) DisconnectPeer(ctx context.Context, peerId string) error  { return nil }
func (m *mockLNClient) GetNewOnchainAddress(ctx context.Context) (string, error) { return "", nil }
func (m *mockLNClient) ResetRouter(key string) error                             { return nil }
func (m *mockLNClient) GetOnchainBalance(ctx context.Context) (*lnclient.OnchainBalanceResponse, error) {
	return nil, nil
}
func (m *mockLNClient) GetBalances(ctx context.Context, includeInactiveChannels bool) (*lnclient.BalancesResponse, error) {
	return nil, nil
}
func (m *mockLNClient) RedeemOnchainFunds(ctx context.Context, toAddress string, amount uint64, feeRate *uint64, sendAll bool) (string, error) {
	return "", nil
}
func (m *mockLNClient) SendPaymentProbes(ctx context.Context, invoice string) error { return nil }
func (m *mockLNClient) SendSpontaneousPaymentProbes(ctx context.Context, amountMloki uint64, nodeId string) error {
	return nil
}
func (m *mockLNClient) ListPeers(ctx context.Context) ([]lnclient.PeerDetails, error) {
	return nil, nil
}
func (m *mockLNClient) GetLogOutput(ctx context.Context, maxLen int) ([]byte, error) { return nil, nil }
func (m *mockLNClient) SignMessage(ctx context.Context, message string) (string, error) {
	return "", nil
}
func (m *mockLNClient) GetStorageDir() (string, error) { return "", nil }
func (m *mockLNClient) GetNetworkGraph(ctx context.Context, nodeIds []string) (lnclient.NetworkGraphResponse, error) {
	return nil, nil
}
func (m *mockLNClient) UpdateLastWalletSyncRequest()                                     {}
func (m *mockLNClient) GetSupportedNIP47Methods() []string                               { return nil }
func (m *mockLNClient) GetSupportedNIP47NotificationTypes() []string                     { return nil }
func (m *mockLNClient) GetCustomNodeCommandDefinitions() []lnclient.CustomNodeCommandDef { return nil }
func (m *mockLNClient) ExecuteCustomNodeCommand(ctx context.Context, command *lnclient.CustomNodeCommandRequest) (*lnclient.CustomNodeCommandResponse, error) {
	return nil, nil
}

func (m *mockLNClient) SetNodeAlias(ctx context.Context, alias string) error {
	return nil
}

func TestLiquidityManager_StartInterceptor(t *testing.T) {
	// Setup
	mockLN := &mockLNClient{
		acceptorChan: make(chan lnclient.ChannelAcceptRequest),
	}

	receivedResponses := make(map[string]struct {
		accept   bool
		zeroConf bool
	})
	mockLN.respondFunc = func(id string, accept bool, zeroConf bool) error {
		receivedResponses[id] = struct {
			accept   bool
			zeroConf bool
		}{accept, zeroConf}
		return nil
	}

	// Setup In-Memory DB for LSPManager
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open valid DB: %v", err)
	}
	// Migrate
	if err := db.AutoMigrate(&persist.LSP{}); err != nil {
		t.Fatalf("Failed to migrate DB: %v", err)
	}

	lspManager := NewLSPManager(db)

	cfg := &ManagerConfig{
		LNClient:   mockLN,
		LSPManager: lspManager,
	}

	// Create manager using internal fields via casting/setup or NewLiquidityManager if possible.
	// NewLiquidityManager returns error if LNClient is nil.
	// But it sets up transport etc.
	// We can construct it manually since we are in same package.

	m := &LiquidityManager{
		cfg:        cfg,
		transport:  transport.NewLNDTransport(mockLN),
		eventQueue: events.NewEventQueue(10),
		listeners:  make(map[string]chan events.Event),
	}

	// Add an Active LSP
	activeLSPPubkey := "03aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	err = m.AddLSP("TrustedLSP", activeLSPPubkey+"@1.2.3.4:5521")
	if err != nil {
		t.Fatalf("AddLSP failed: %v", err)
	}
	err = m.AddSelectedLSP(activeLSPPubkey)
	if err != nil {
		t.Fatalf("AddSelectedLSP failed: %v", err)
	}

	// Add an Inactive LSP
	inactiveLSPPubkey := "03bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	err = m.AddLSP("OtherLSP", inactiveLSPPubkey+"@1.2.3.4:5521")
	if err != nil {
		t.Fatalf("AddLSP failed: %v", err)
	}
	// Not selected

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run Interceptor
	go m.StartInterceptor(ctx)

	// Wait for subscription
	time.Sleep(100 * time.Millisecond)
	if !mockLN.subscribeCalled {
		t.Fatal("Interceptor did not subscribe")
	}

	// 1. Test Trusted/Active LSP -> Expect ZeroConf=true
	req1 := lnclient.ChannelAcceptRequest{
		ID:         "01",
		NodePubkey: activeLSPPubkey,
		Capacity:   100000,
	}
	mockLN.acceptorChan <- req1

	time.Sleep(50 * time.Millisecond)

	resp1, ok := receivedResponses["01"]
	if !ok {
		t.Fatal("Did not receive response for req1")
	}
	if !resp1.accept {
		t.Error("Expected req1 to be accepted")
	}
	if !resp1.zeroConf {
		t.Error("Expected req1 to be ZeroConf")
	}

	// 2. Test Inactive LSP -> Expect ZeroConf=false
	req2 := lnclient.ChannelAcceptRequest{
		ID:         "02",
		NodePubkey: inactiveLSPPubkey,
		Capacity:   100000,
	}
	mockLN.acceptorChan <- req2

	time.Sleep(50 * time.Millisecond)

	resp2, ok := receivedResponses["02"]
	if !ok {
		t.Fatal("Did not receive response for req2")
	}
	if !resp2.accept {
		t.Error("Expected req2 to be accepted")
	}
	if resp2.zeroConf {
		t.Error("Expected req2 to NOT be ZeroConf")
	}

	// 3. Test Unknown Peer -> Expect ZeroConf=false
	req3 := lnclient.ChannelAcceptRequest{
		ID:         "03",
		NodePubkey: "03cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Capacity:   100000,
	}
	mockLN.acceptorChan <- req3

	time.Sleep(50 * time.Millisecond)

	resp3, ok := receivedResponses["03"]
	if !ok {
		t.Fatal("Did not receive response for req3")
	}
	if !resp3.accept {
		t.Error("Expected req3 to be accepted")
	}
	if resp3.zeroConf {
		t.Error("Expected req3 to NOT be ZeroConf")
	}

	// 4. Test Mixed Case Active LSP Selection -> Expect ZeroConf=true
	// Add mixed case LSP
	mixedCasePubkey := "03DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
	m.AddLSP("MixedLSP", mixedCasePubkey+"@1.2.3.4:5521")
	m.AddSelectedLSP(mixedCasePubkey) // Should normalize

	req4 := lnclient.ChannelAcceptRequest{
		ID: "04",
		// Request comes in lowercase usually from LND (hex encoded), but strictly checking if handle logic normalizes request too
		NodePubkey: strings.ToLower(mixedCasePubkey),
		Capacity:   100000,
	}
	mockLN.acceptorChan <- req4

	time.Sleep(50 * time.Millisecond)

	resp4, ok := receivedResponses["04"]
	if !ok {
		t.Fatal("Did not receive response for req4")
	}
	if !resp4.accept {
		t.Error("Expected req4 to be accepted")
	}
	if !resp4.zeroConf {
		t.Error("Expected req4 to be ZeroConf (Mixed Case LSP should be matched)")
	}
}
