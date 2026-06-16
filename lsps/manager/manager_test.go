package manager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/flokiorg/lokihub/lsps/transport"
	"github.com/stretchr/testify/assert"
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

	var responsesMu sync.Mutex
	receivedResponses := make(map[string]struct {
		accept   bool
		zeroConf bool
	})
	mockLN.respondFunc = func(id string, accept bool, zeroConf bool) error {
		responsesMu.Lock()
		receivedResponses[id] = struct {
			accept   bool
			zeroConf bool
		}{accept, zeroConf}
		responsesMu.Unlock()
		return nil
	}

	// Signal when the interceptor has subscribed for the first time.
	subscribedCh := make(chan struct{}, 1)
	mockLN.subscribeCallback = func() {
		select {
		case subscribedCh <- struct{}{}:
		default:
		}
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
		transport:  transport.NewFLNDTransport(mockLN),
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

	// Wait for subscription (via callback channel instead of reading the bool field).
	select {
	case <-subscribedCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Interceptor did not subscribe")
	}

	getResp := func(id string) (struct{ accept, zeroConf bool }, bool) {
		responsesMu.Lock()
		defer responsesMu.Unlock()
		r, ok := receivedResponses[id]
		return r, ok
	}

	// 1. Test Trusted/Active LSP -> Expect ZeroConf=true
	req1 := lnclient.ChannelAcceptRequest{
		ID:         "01",
		NodePubkey: activeLSPPubkey,
		Capacity:   100000,
	}
	mockLN.acceptorChan <- req1

	assert.Eventually(t, func() bool { _, ok := getResp("01"); return ok }, 500*time.Millisecond, 10*time.Millisecond)
	resp1, _ := getResp("01")
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

	assert.Eventually(t, func() bool { _, ok := getResp("02"); return ok }, 500*time.Millisecond, 10*time.Millisecond)
	resp2, _ := getResp("02")
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

	assert.Eventually(t, func() bool { _, ok := getResp("03"); return ok }, 500*time.Millisecond, 10*time.Millisecond)
	resp3, _ := getResp("03")
	if !resp3.accept {
		t.Error("Expected req3 to be accepted")
	}
	if resp3.zeroConf {
		t.Error("Expected req3 to NOT be ZeroConf")
	}

	// 4. Test Mixed Case Active LSP Selection -> Expect ZeroConf=true
	mixedCasePubkey := "03DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
	m.AddLSP("MixedLSP", mixedCasePubkey+"@1.2.3.4:5521")
	m.AddSelectedLSP(mixedCasePubkey)

	req4 := lnclient.ChannelAcceptRequest{
		ID:         "04",
		NodePubkey: strings.ToLower(mixedCasePubkey),
		Capacity:   100000,
	}
	mockLN.acceptorChan <- req4

	assert.Eventually(t, func() bool { _, ok := getResp("04"); return ok }, 500*time.Millisecond, 10*time.Millisecond)
	resp4, _ := getResp("04")
	if !resp4.accept {
		t.Error("Expected req4 to be accepted")
	}
	if !resp4.zeroConf {
		t.Error("Expected req4 to be ZeroConf (Mixed Case LSP should be matched)")
	}
}

// mockInterceptorClient wraps mockLNClient with per-call SubscribeChannelAcceptor
// control, enabling tests for initial-failure retry and mid-stream reconnect.
type mockInterceptorClient struct {
	mockLNClient
	calls    atomic.Int32
	builders []func() (<-chan lnclient.ChannelAcceptRequest, func(string, bool, bool) error, error)
}

func (m *mockInterceptorClient) SubscribeChannelAcceptor(ctx context.Context) (<-chan lnclient.ChannelAcceptRequest, func(id string, accept bool, zeroConf bool) error, error) {
	idx := int(m.calls.Add(1)) - 1
	if idx < len(m.builders) {
		return m.builders[idx]()
	}
	// Default: block until context cancelled.
	ch := make(chan lnclient.ChannelAcceptRequest)
	go func() { <-ctx.Done(); close(ch) }()
	return ch, func(string, bool, bool) error { return nil }, nil
}

func newInterceptorManager(t *testing.T, client lnclient.LNClient) *LiquidityManager {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s_icpt?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&persist.LSP{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &LiquidityManager{
		cfg:             &ManagerConfig{LNClient: client, LSPManager: NewLSPManager(db)},
		transport:       transport.NewFLNDTransport(client),
		eventQueue:      events.NewEventQueue(10),
		listeners:       make(map[string]chan events.Event),
		unclaimedEvents: make(map[string]events.Event),
		nostrPubkeys:    make(map[string]string),
	}
}

func TestStartInterceptor_RetriesOnInitialFailure(t *testing.T) {
	openCh := make(chan lnclient.ChannelAcceptRequest)
	respond := func(string, bool, bool) error { return nil }

	client := &mockInterceptorClient{
		builders: []func() (<-chan lnclient.ChannelAcceptRequest, func(string, bool, bool) error, error){
			func() (<-chan lnclient.ChannelAcceptRequest, func(string, bool, bool) error, error) {
				return nil, nil, fmt.Errorf("node not ready yet")
			},
			func() (<-chan lnclient.ChannelAcceptRequest, func(string, bool, bool) error, error) {
				return openCh, respond, nil
			},
		},
	}

	m := newInterceptorManager(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go m.StartInterceptor(ctx)

	// The second subscription must be established within the retry window (5s + margin).
	assert.Eventually(t, func() bool {
		return client.calls.Load() >= 2
	}, 12*time.Second, 100*time.Millisecond, "StartInterceptor should retry after initial subscription failure")
}

func TestStartInterceptor_ReconnectsAfterStreamClose(t *testing.T) {
	// First channel is closed immediately (simulates mid-stream close).
	closedCh := make(chan lnclient.ChannelAcceptRequest)
	close(closedCh)

	openCh := make(chan lnclient.ChannelAcceptRequest)
	respond := func(string, bool, bool) error { return nil }

	client := &mockInterceptorClient{
		builders: []func() (<-chan lnclient.ChannelAcceptRequest, func(string, bool, bool) error, error){
			func() (<-chan lnclient.ChannelAcceptRequest, func(string, bool, bool) error, error) {
				return closedCh, respond, nil
			},
			func() (<-chan lnclient.ChannelAcceptRequest, func(string, bool, bool) error, error) {
				return openCh, respond, nil
			},
		},
	}

	m := newInterceptorManager(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go m.StartInterceptor(ctx)

	assert.Eventually(t, func() bool {
		return client.calls.Load() >= 2
	}, 12*time.Second, 100*time.Millisecond, "StartInterceptor should resubscribe after stream channel closes")
}

func TestStartInterceptor_StopsOnContextCancel(t *testing.T) {
	blockCh := make(chan lnclient.ChannelAcceptRequest)

	client := &mockInterceptorClient{
		builders: []func() (<-chan lnclient.ChannelAcceptRequest, func(string, bool, bool) error, error){
			func() (<-chan lnclient.ChannelAcceptRequest, func(string, bool, bool) error, error) {
				return blockCh, func(string, bool, bool) error { return nil }, nil
			},
		},
	}

	m := newInterceptorManager(t, client)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		m.StartInterceptor(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("StartInterceptor did not stop after context cancel")
	}
}

func TestHandleChannelAcceptRequest_LogsRespondError(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s_resp_err?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&persist.LSP{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	m := &LiquidityManager{
		cfg:          &ManagerConfig{LSPManager: NewLSPManager(db)},
		nostrPubkeys: make(map[string]string),
	}

	respond := func(_ string, _ bool, _ bool) error {
		return fmt.Errorf("grpc: connection closed")
	}

	req := lnclient.ChannelAcceptRequest{ID: "err-01", NodePubkey: "unknown-peer"}

	// Must not panic even when respond() returns an error.
	assert.NotPanics(t, func() {
		m.handleChannelAcceptRequest(req, respond)
	})
}

func TestHandleChannelAcceptRequest_DBErrorAcceptsWithoutZeroConf(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s_db_err?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&persist.LSP{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	m := &LiquidityManager{
		cfg:          &ManagerConfig{LSPManager: NewLSPManager(db)},
		nostrPubkeys: make(map[string]string),
	}

	// Close the underlying connection so getLSPsFromDB returns an error.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.Close()

	var respondedAccept, respondedZeroConf bool
	respond := func(_ string, accept bool, zeroConf bool) error {
		respondedAccept = accept
		respondedZeroConf = zeroConf
		return nil
	}

	req := lnclient.ChannelAcceptRequest{
		ID:         "dberr-01",
		NodePubkey: "03aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	m.handleChannelAcceptRequest(req, respond)

	assert.True(t, respondedAccept, "channel must be accepted even when DB errors (fail-safe)")
	assert.False(t, respondedZeroConf, "ZeroConf must be denied when whitelist cannot be consulted due to DB error")
}
