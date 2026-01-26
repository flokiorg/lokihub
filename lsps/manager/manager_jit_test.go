package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/lsps0"
	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/lsps5"
	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/flokiorg/lokihub/lsps/transport"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// mockLNClientJIT extends functionality for JIT testing
type mockLNClientJIT struct {
	// Embed the original mock or re-implement
	// re-implementing relevant parts for simplicity
	msgChan  chan lnclient.CustomMessage
	errChan  chan error
	sentMsgs []lnclient.CustomMessage
	balances *lnclient.BalancesResponse
	mu       sync.Mutex // Protect sentMsgs
}

func (m *mockLNClientJIT) SubscribeCustomMessages(ctx context.Context) (<-chan lnclient.CustomMessage, <-chan error, error) {
	return m.msgChan, m.errChan, nil
}

func (m *mockLNClientJIT) SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentMsgs = append(m.sentMsgs, lnclient.CustomMessage{
		PeerPubkey: peerPubkey,
		Type:       msgType,
		Data:       data,
	})
	return nil
}

func (m *mockLNClientJIT) GetBalances(ctx context.Context, includeInactiveChannels bool) (*lnclient.BalancesResponse, error) {
	if m.balances != nil {
		return m.balances, nil
	}
	return &lnclient.BalancesResponse{
		Lightning: lnclient.LightningBalanceResponse{
			TotalReceivable: 0,
		},
	}, nil
}

// Stubs
func (m *mockLNClientJIT) SubscribeChannelAcceptor(ctx context.Context) (<-chan lnclient.ChannelAcceptRequest, func(id string, accept bool, zeroConf bool) error, error) {
	return nil, nil, nil
}
func (m *mockLNClientJIT) SendPaymentSync(payReq string, amount *uint64) (*lnclient.PayInvoiceResponse, error) {
	return nil, nil
}
func (m *mockLNClientJIT) SendKeysend(amount uint64, destination string, customRecords []lnclient.TLVRecord, preimage string) (*lnclient.PayKeysendResponse, error) {
	return nil, nil
}
func (m *mockLNClientJIT) GetPubkey() string                                       { return "" }
func (m *mockLNClientJIT) GetInfo(ctx context.Context) (*lnclient.NodeInfo, error) { return nil, nil }
func (m *mockLNClientJIT) MakeInvoice(ctx context.Context, amount int64, description string, descriptionHash string, expiry int64, throughNodePubkey *string, lspJitChannelSCID *string, lspCltvExpiryDelta *uint16, lspFeeBaseMloki *uint64, lspFeeProportionalMillionths *uint32) (*lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClientJIT) MakeHoldInvoice(ctx context.Context, amount int64, description string, descriptionHash string, expiry int64, paymentHash string) (*lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClientJIT) SettleHoldInvoice(ctx context.Context, preimage string) error { return nil }
func (m *mockLNClientJIT) CancelHoldInvoice(ctx context.Context, paymentHash string) error {
	return nil
}
func (m *mockLNClientJIT) LookupInvoice(ctx context.Context, paymentHash string) (*lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClientJIT) ListTransactions(ctx context.Context, from, until, limit, offset uint64, unpaid bool, invoiceType string) ([]lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClientJIT) ListOnchainTransactions(ctx context.Context, from, until, limit, offset uint64) ([]lnclient.OnchainTransaction, error) {
	return nil, nil
}
func (m *mockLNClientJIT) Shutdown() error { return nil }
func (m *mockLNClientJIT) ListChannels(ctx context.Context) ([]lnclient.Channel, error) {
	return nil, nil
}
func (m *mockLNClientJIT) GetNodeConnectionInfo(ctx context.Context) (*lnclient.NodeConnectionInfo, error) {
	return nil, nil
}
func (m *mockLNClientJIT) GetNodeStatus(ctx context.Context) (*lnclient.NodeStatus, error) {
	return nil, nil
}
func (m *mockLNClientJIT) ConnectPeer(ctx context.Context, connectPeerRequest *lnclient.ConnectPeerRequest) error {
	return nil
}
func (m *mockLNClientJIT) OpenChannel(ctx context.Context, openChannelRequest *lnclient.OpenChannelRequest) (*lnclient.OpenChannelResponse, error) {
	return nil, nil
}
func (m *mockLNClientJIT) CloseChannel(ctx context.Context, closeChannelRequest *lnclient.CloseChannelRequest) (*lnclient.CloseChannelResponse, error) {
	return nil, nil
}
func (m *mockLNClientJIT) UpdateChannel(ctx context.Context, updateChannelRequest *lnclient.UpdateChannelRequest) error {
	return nil
}
func (m *mockLNClientJIT) DisconnectPeer(ctx context.Context, peerId string) error  { return nil }
func (m *mockLNClientJIT) GetNewOnchainAddress(ctx context.Context) (string, error) { return "", nil }
func (m *mockLNClientJIT) ResetRouter(key string) error                             { return nil }
func (m *mockLNClientJIT) GetOnchainBalance(ctx context.Context) (*lnclient.OnchainBalanceResponse, error) {
	return nil, nil
}
func (m *mockLNClientJIT) RedeemOnchainFunds(ctx context.Context, toAddress string, amount uint64, feeRate *uint64, sendAll bool) (string, error) {
	return "", nil
}
func (m *mockLNClientJIT) SendPaymentProbes(ctx context.Context, invoice string) error { return nil }
func (m *mockLNClientJIT) SendSpontaneousPaymentProbes(ctx context.Context, amountMloki uint64, nodeId string) error {
	return nil
}
func (m *mockLNClientJIT) ListPeers(ctx context.Context) ([]lnclient.PeerDetails, error) {
	return nil, nil
}
func (m *mockLNClientJIT) GetLogOutput(ctx context.Context, maxLen int) ([]byte, error) {
	return nil, nil
}
func (m *mockLNClientJIT) SignMessage(ctx context.Context, message string) (string, error) {
	return "", nil
}
func (m *mockLNClientJIT) GetStorageDir() (string, error) { return "", nil }
func (m *mockLNClientJIT) GetNetworkGraph(ctx context.Context, nodeIds []string) (lnclient.NetworkGraphResponse, error) {
	return nil, nil
}
func (m *mockLNClientJIT) UpdateLastWalletSyncRequest()                 {}
func (m *mockLNClientJIT) GetSupportedNIP47Methods() []string           { return nil }
func (m *mockLNClientJIT) GetSupportedNIP47NotificationTypes() []string { return nil }
func (m *mockLNClientJIT) GetCustomNodeCommandDefinitions() []lnclient.CustomNodeCommandDef {
	return nil
}
func (m *mockLNClientJIT) ExecuteCustomNodeCommand(ctx context.Context, command *lnclient.CustomNodeCommandRequest) (*lnclient.CustomNodeCommandResponse, error) {
	return nil, nil
}
func (m *mockLNClientJIT) SetNodeAlias(ctx context.Context, alias string) error { return nil }

func TestEnsureInboundLiquidity_RetryOnStaleParams(t *testing.T) {
	jitSCID := uint64(8796093087745)
	// Setup DB
	db, _ := gorm.Open(sqlite.Open("file:memdb_jit?mode=memory&cache=shared"), &gorm.Config{})
	db.AutoMigrate(&persist.LSP{})
	lspManager := NewLSPManager(db)

	mockLN := &mockLNClientJIT{
		msgChan:  make(chan lnclient.CustomMessage, 10),
		errChan:  make(chan error),
		balances: &lnclient.BalancesResponse{Lightning: lnclient.LightningBalanceResponse{TotalReceivable: 0}}, // 0 inbound
	}

	cfg := &ManagerConfig{LNClient: mockLN, LSPManager: lspManager}
	// Manually construct manager to use our mock interactions
	m := &LiquidityManager{
		cfg:         cfg,
		transport:   transport.NewLNDTransport(mockLN),
		eventQueue:  events.NewEventQueue(10),
		listeners:   make(map[string]chan events.Event),
		lsps2Client: lsps2.NewClientHandler(transport.NewLNDTransport(mockLN), events.NewEventQueue(10)), // Need m.lsps2Client set
	}
	// Fix: m.lsps2Client needs to use the SAME event queue as processInternalEvents
	m.lsps2Client = lsps2.NewClientHandler(m.transport, m.eventQueue)
	m.lsps0Client = lsps0.NewClientHandler(m.transport, m.eventQueue)
	m.lsps1Client = lsps1.NewClientHandler(m.transport, m.eventQueue)
	m.lsps5Client = lsps5.NewClientHandler(m.transport, m.eventQueue)
	m.unclaimedEvents = make(map[string]events.Event)

	// Add Active LSP
	lspPubkey := "03aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	err := m.AddLSP("TestLSP", lspPubkey+"@host:9735")
	if err != nil {
		t.Fatalf("AddLSP failed: %v", err)
	}
	err = m.AddSelectedLSP(lspPubkey)
	if err != nil {
		t.Fatalf("AddSelectedLSP failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start processing events in background
	go m.processInternalEvents(ctx)
	// Also start processing messages (CRITICAL)
	msgChan, errChanMock, _ := mockLN.SubscribeCustomMessages(ctx)
	go m.processMessages(ctx, msgChan, errChanMock)

	// We run EnsureInboundLiquidity in a goroutine because it blocks waiting for responses
	resultChan := make(chan *JitChannelHints, 1)
	errChan := make(chan error, 1)

	go func() {
		hints, err := m.EnsureInboundLiquidity(ctx, 100000)
		if err != nil {
			errChan <- err
		} else {
			resultChan <- hints
		}
	}()

	// SIMULATION LOGIC
	// 1. First request will be GetInfo
	// We wait for it to be sent (check mockLN.sentMsgs or create a hook? polling is easier for test)

	// waitForMsg helper that fails fast on error
	waitForMsg := func() (lnclient.CustomMessage, *lsps0.JsonRpcRequest) {
		for {
			select {
			case err := <-errChan:
				t.Fatalf("EnsureInboundLiquidity returned error: %v", err)
				return lnclient.CustomMessage{}, nil
			case <-time.After(10 * time.Millisecond):
				// check for messages
				mockLN.mu.Lock()
				if len(mockLN.sentMsgs) > 0 {
					msg := mockLN.sentMsgs[0]
					mockLN.sentMsgs = mockLN.sentMsgs[1:]
					mockLN.mu.Unlock()
					// Decode JSON-RPC
					var req lsps0.JsonRpcRequest
					_ = json.Unmarshal(msg.Data, &req)
					return msg, &req
				}
				mockLN.mu.Unlock()
			case <-ctx.Done():
				return lnclient.CustomMessage{}, nil
			}
		}
	}

	// 1. Expect GetInfo
	_, req1 := waitForMsg()
	if req1 == nil {
		t.Fatal("Timeout waiting for GetInfo")
	}
	assert.Equal(t, lsps2.MethodGetInfo, req1.Method)

	// Reply with Valid Params
	validUntil := time.Now().Add(1 * time.Hour)
	paramsMenu := []lsps2.OpeningFeeParams{
		{MinFeeMloki: 100, ValidUntil: validUntil, MinPaymentSizeMloki: 100, MaxPaymentSizeMloki: 10000000},
	}
	resp1 := lsps0.JsonRpcResponse{
		Jsonrpc: "2.0",
		ID:      req1.ID,
		Result:  lsps2.GetInfoResponse{OpeningFeeParamsMenu: paramsMenu},
	}
	data1, _ := json.Marshal(resp1)
	mockLN.msgChan <- lnclient.CustomMessage{PeerPubkey: lspPubkey, Type: lsps0.LSPS_MESSAGE_TYPE_ID, Data: data1}

	// 2. Expect Buy Request
	_, req2 := waitForMsg()
	if req2 == nil {
		t.Fatal("Timeout waiting for Buy")
	}
	assert.Equal(t, lsps2.MethodBuy, req2.Method)

	// Reply with ERROR 201 (Stale/Invalid Params)
	errResp := lsps0.JsonRpcResponse{
		Jsonrpc: "2.0",
		ID:      req2.ID,
		Error:   &lsps0.JsonRpcError{Code: 201, Message: "invalid_opening_fee_params"},
	}
	dataErr, _ := json.Marshal(errResp)
	mockLN.msgChan <- lnclient.CustomMessage{PeerPubkey: lspPubkey, Type: lsps0.LSPS_MESSAGE_TYPE_ID, Data: dataErr}

	// 3. Expect GetInfo AGAIN (Retry)
	_, req3 := waitForMsg()
	if req3 == nil {
		t.Fatal("Timeout waiting for Retry GetInfo")
	}
	assert.Equal(t, lsps2.MethodGetInfo, req3.Method)

	// Reply with Fresh Params
	resp3 := maxCopy(resp1) // Reuse same params for simplicity
	resp3.ID = req3.ID
	data3, _ := json.Marshal(resp3)
	mockLN.msgChan <- lnclient.CustomMessage{PeerPubkey: lspPubkey, Type: lsps0.LSPS_MESSAGE_TYPE_ID, Data: data3}

	// 4. Expect Buy Request AGAIN
	_, req4 := waitForMsg()
	if req4 == nil {
		t.Fatal("Timeout waiting for Retry Buy")
	}
	assert.Equal(t, lsps2.MethodBuy, req4.Method)

	// Reply with Success
	// SCID format must be blockxindexxoutput for ParseSCID to work
	buyResult := lsps2.BuyResponse{
		JitChannelSCID: "800000x1x1",
		LSPNodeID:      lspPubkey,
	}
	// Need to ensure SCID is parsed correctly (string vs uint64 in response? client.go checks type?)
	// lsps2/client.go: func (r *BuyResponse) ParseSCID() ... maps scid string to uint64
	// So response should have string JitChannelSCID.
	resp4 := lsps0.JsonRpcResponse{
		Jsonrpc: "2.0",
		ID:      req4.ID,
		Result:  buyResult,
	}
	data4, _ := json.Marshal(resp4)
	mockLN.msgChan <- lnclient.CustomMessage{PeerPubkey: lspPubkey, Type: lsps0.LSPS_MESSAGE_TYPE_ID, Data: data4}

	// 5. Verify Result
	select {
	case hints := <-resultChan:
		assert.NotNil(t, hints)
		assert.Equal(t, fmt.Sprintf("%d", jitSCID), hints.SCID)
		assert.Equal(t, lspPubkey, hints.LSPNodeID)
		t.Log("Successfully handled JIT retry sequence!")
	case err := <-errChan:
		t.Fatalf("EnsureInboundLiquidity failed unexpectedly: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for result")
	}
}

func maxCopy(r lsps0.JsonRpcResponse) lsps0.JsonRpcResponse {
	return r
}
