package lsps1

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/lsps0"
	"github.com/flokiorg/lokihub/lsps/transport"
)

// Reusing MockLNClient from transport test logic but inline here for simplicity due to package boundaries
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

// Implement other methods as no-ops... (omitted for brevity, assume strict subset usage)
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

func TestClient_RequestSupportedOptions(t *testing.T) {
	mockLN := newMockLNClient()
	transport := transport.NewLNDTransport(mockLN)
	eq := events.NewEventQueue(10)
	client := NewClientHandler(transport, eq)

	ctx := context.Background()
	peer := "peer1"

	// 1. Send Request
	reqID, err := client.RequestSupportedOptions(ctx, peer)
	if err != nil {
		t.Fatalf("RequestSupportedOptions failed: %v", err)
	}

	if len(mockLN.sendCalls) != 1 {
		t.Fatalf("Expected 1 send call, got %d", len(mockLN.sendCalls))
	}

	// Verify request content
	var req lsps0.JsonRpcRequest
	if err := json.Unmarshal(mockLN.sendCalls[0].data, &req); err != nil {
		t.Fatalf("Failed to unmarshal request: %v", err)
	}
	if req.Method != MethodGetInfo {
		t.Errorf("Expected method %s, got %s", MethodGetInfo, req.Method)
	}
	if req.ID != reqID {
		t.Errorf("Expected ID %s, got %s", reqID, req.ID)
	}

	// 2. Mock Response
	resp := lsps0.JsonRpcResponse{
		Jsonrpc: "2.0",
		ID:      reqID,
		Result: Options{
			MinRequiredChannelConfirmations: 1,
			MinFundingConfirmsWithinBlocks:  6,
			SupportsZeroChannelReserve:      true,
			MinInitialClientBalanceLoki:     10000,
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

	readyEv, ok := ev.(*SupportedOptionsReadyEvent)
	if !ok {
		t.Fatal("Expected SupportedOptionsReadyEvent")
	}
	if readyEv.RequestID != reqID {
		t.Errorf("Expected RequestID %s, got %s", reqID, readyEv.RequestID)
	}
	if readyEv.SupportedOptions.MinInitialClientBalanceLoki != 10000 {
		t.Errorf("Expected MinInitialClientBalanceLoki 10000, got %d", readyEv.SupportedOptions.MinInitialClientBalanceLoki)
	}
}

func TestClient_CreateOrder(t *testing.T) {
	mockLN := newMockLNClient()
	transport := transport.NewLNDTransport(mockLN)
	eq := events.NewEventQueue(10)
	client := NewClientHandler(transport, eq)

	ctx := context.Background()
	peer := "peer1"

	order := OrderParams{
		LspBalanceLoki:    100000,
		ClientBalanceLoki: 10000,
	}

	// 1. Send Request
	reqID, err := client.CreateOrder(ctx, peer, order, nil)
	if err != nil {
		t.Fatalf("CreateOrder failed: %v", err)
	}

	// 2. Mock Response
	resp := lsps0.JsonRpcResponse{
		Jsonrpc: "2.0",
		ID:      reqID,
		Result: CreateOrderResponse{
			OrderID: "order123",
			Order:   order,
			Payment: PaymentInfo{
				Bolt11: &Bolt11PaymentInfo{
					State:   "EXPECT_PAYMENT",
					Invoice: "lnbc...",
				},
			},
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

	createdEv, ok := ev.(*OrderCreatedEvent)
	if !ok {
		t.Fatal("Expected OrderCreatedEvent")
	}
	if createdEv.OrderID != "order123" {
		t.Errorf("Expected OrderID order123, got %s", createdEv.OrderID)
	}
	if createdEv.Payment.Bolt11 == nil {
		t.Error("Expected Bolt11 payment info")
	}
}
