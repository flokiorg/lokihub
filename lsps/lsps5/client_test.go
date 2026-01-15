package lsps5

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/lsps0"
	"github.com/flokiorg/lokihub/lsps/transport"
)

// Reusing MockLNClient logic
type mockLNClient struct {
	sendCalls []sendCall
	msgChan   chan lnclient.CustomMessage
	errChan   chan error
}

type sendCall struct {
	peerPubkey string
	msgType    uint32
	data       []byte
}

func newMockLNClient() *mockLNClient {
	return &mockLNClient{
		sendCalls: []sendCall{},
		msgChan:   make(chan lnclient.CustomMessage, 10),
		errChan:   make(chan error, 1),
	}
}

func (m *mockLNClient) SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error {
	m.sendCalls = append(m.sendCalls, sendCall{peerPubkey, msgType, data})
	return nil
}

func (m *mockLNClient) SubscribeCustomMessages(ctx context.Context) (<-chan lnclient.CustomMessage, <-chan error, error) {
	return m.msgChan, m.errChan, nil
}

// No-ops for other methods
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

func (m *mockLNClient) SubscribeChannelAcceptor(ctx context.Context) (<-chan lnclient.ChannelAcceptRequest, func(id string, accept bool, zeroConf bool) error, error) {
	return nil, nil, nil
}

func TestClient_SetWebhook(t *testing.T) {
	mockLN := newMockLNClient()
	transport := transport.NewLNDTransport(mockLN)
	eq := events.NewEventQueue(10)
	client := NewClientHandler(transport, eq)

	ctx := context.Background()
	peer := "peer1"
	appName := "MyApp"
	webhook := "https://example.com/hook"

	// 1. Send SetWebhook
	reqID, err := client.SetWebhook(ctx, peer, appName, webhook)
	if err != nil {
		t.Fatalf("SetWebhook failed: %v", err)
	}

	if len(mockLN.sendCalls) != 1 {
		t.Fatalf("Expected 1 send call, got %d", len(mockLN.sendCalls))
	}

	var req lsps0.JsonRpcRequest
	if err := json.Unmarshal(mockLN.sendCalls[0].data, &req); err != nil {
		t.Fatalf("Failed to unmarshal request: %v", err)
	}
	if req.Method != MethodSetWebhook {
		t.Errorf("Expected method %s, got %s", MethodSetWebhook, req.Method)
	}

	// 2. Mock Response
	resp := lsps0.JsonRpcResponse{
		Jsonrpc: "2.0",
		ID:      reqID,
		Result: SetWebhookResponse{
			NumWebhooks: 1,
			MaxWebhooks: 5,
			NoChange:    false,
		},
	}
	respBytes, _ := json.Marshal(resp)

	// 3. Handle Message
	if err := client.HandleMessage(peer, respBytes); err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	// 4. Verify Event
	ev, err := eq.NextEvent(ctx)
	if err != nil {
		t.Fatalf("NextEvent failed: %v", err)
	}

	regEv, ok := ev.(*WebhookRegisteredEvent)
	if !ok {
		t.Fatal("Expected WebhookRegisteredEvent")
	}
	if regEv.RequestID != reqID {
		t.Errorf("Expected RequestID %s, got %s", reqID, regEv.RequestID)
	}
	if regEv.AppName != appName {
		t.Errorf("Expected AppName %s, got %s", appName, regEv.AppName)
	}
}

func TestClient_ListWebhooks(t *testing.T) {
	mockLN := newMockLNClient()
	transport := transport.NewLNDTransport(mockLN)
	eq := events.NewEventQueue(10)
	client := NewClientHandler(transport, eq)

	ctx := context.Background()
	peer := "peer1"

	// 1. Send ListWebhooks
	reqID, err := client.ListWebhooks(ctx, peer)
	if err != nil {
		t.Fatalf("ListWebhooks failed: %v", err)
	}

	// 2. Mock Response
	resp := lsps0.JsonRpcResponse{
		Jsonrpc: "2.0",
		ID:      reqID,
		Result: ListWebhooksResponse{
			AppNames:    []string{"MyApp"},
			MaxWebhooks: 5,
		},
	}
	respBytes, _ := json.Marshal(resp)

	// 3. Handle Message
	if err := client.HandleMessage(peer, respBytes); err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	// 4. Verify Event
	ev, err := eq.NextEvent(ctx)
	if err != nil {
		t.Fatalf("NextEvent failed: %v", err)
	}

	listEv, ok := ev.(*WebhooksListedEvent)
	if !ok {
		t.Fatal("Expected WebhooksListedEvent")
	}
	if len(listEv.AppNames) != 1 || listEv.AppNames[0] != "MyApp" {
		t.Errorf("Expected [MyApp], got %v", listEv.AppNames)
	}
}
