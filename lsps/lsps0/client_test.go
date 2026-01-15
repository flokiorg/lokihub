package lsps0

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/transport"
)

// Reusing MockLNClient logic (simplified)
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

// implement dummy required methods
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

func TestClient_ListProtocols(t *testing.T) {
	mockLN := newMockLNClient()
	transport := transport.NewLNDTransport(mockLN)
	eq := events.NewEventQueue(10)
	client := NewClientHandler(transport, eq)

	ctx := context.Background()
	peer := "peer1"

	// 1. Send Request (Async but we mock response)
	// Just verify request encoding and sending

	go func() {
		// Simulate network delay and response
		// In a real test we'd need synchronization to know when request was sent
		// For now we just assume it's quick or we rely on the channel receive blocking
	}()

	// We need to be able to catch the outgoing message to respond to it with the correct ID.
	// But `ListProtocols` is synchronous-looking (blocks on response channel).
	// So we need to mock the transport to handle the send *and trigger the response*.
	// But my mock `SendCustomMessage` just appends to a slice.

	// Better approach for this synchronous client test:
	// Start a goroutine that watches `mockLN.sendCalls` and replies.

	stopCh := make(chan struct{})
	defer close(stopCh)

	go func() {
		for {
			select {
			case <-stopCh:
				return
			default:
				if len(mockLN.sendCalls) > 0 {
					call := mockLN.sendCalls[0]
					var req JsonRpcRequest
					if err := json.Unmarshal(call.data, &req); err == nil {
						// Respond
						resp := JsonRpcResponse{
							Jsonrpc: "2.0",
							ID:      req.ID,
							Result: ListProtocolsResponse{
								Protocols: []int{1, 2},
							},
						}
						respBytes, _ := json.Marshal(resp)
						client.HandleMessage(peer, respBytes)
						return // Done
					}
				}
			}
		}
	}()

	protocols, err := client.ListProtocols(ctx, peer)
	if err != nil {
		t.Fatalf("ListProtocols failed: %v", err)
	}

	if len(protocols) != 2 {
		t.Errorf("Expected 2 protocols, got %d", len(protocols))
	}
}
