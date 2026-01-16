package transport

import (
	"context"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lnclient"
)

// Mock LNClient for testing
type mockLNClient struct {
	sendCalls      []sendCall
	msgChan        chan lnclient.CustomMessage
	errChan        chan error
	shouldFailSend bool
	shouldFailSub  bool
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
	if m.shouldFailSend {
		return context.Canceled
	}
	m.sendCalls = append(m.sendCalls, sendCall{peerPubkey, msgType, data})
	return nil
}

func (m *mockLNClient) SubscribeCustomMessages(ctx context.Context) (<-chan lnclient.CustomMessage, <-chan error, error) {
	if m.shouldFailSub {
		return nil, nil, context.Canceled
	}
	return m.msgChan, m.errChan, nil
}

// Implement other LNClient methods as no-ops
func (m *mockLNClient) SendPaymentSync(payReq string, amount *uint64) (*lnclient.PayInvoiceResponse, error) {
	return nil, nil
}
func (m *mockLNClient) SendKeysend(amount uint64, destination string, customRecords []lnclient.TLVRecord, preimage string) (*lnclient.PayKeysendResponse, error) {
	return nil, nil
}
func (m *mockLNClient) GetPubkey() string { return "" }
func (m *mockLNClient) GetInfo(ctx context.Context) (*lnclient.NodeInfo, error) {
	return nil, nil
}
func (m *mockLNClient) MakeInvoice(ctx context.Context, amount int64, description string, descriptionHash string, expiry int64, throughNodePubkey *string, lspJitChannelSCID *string, lspCltvExpiryDelta *uint16, lspFeeBaseMloki *uint64, lspFeeProportionalMillionths *uint32) (*lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClient) MakeHoldInvoice(ctx context.Context, amount int64, description string, descriptionHash string, expiry int64, paymentHash string) (*lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClient) SettleHoldInvoice(ctx context.Context, preimage string) error { return nil }
func (m *mockLNClient) CancelHoldInvoice(ctx context.Context, paymentHash string) error {
	return nil
}
func (m *mockLNClient) LookupInvoice(ctx context.Context, paymentHash string) (*lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClient) ListTransactions(ctx context.Context, from, until, limit, offset uint64, unpaid bool, invoiceType string) ([]lnclient.Transaction, error) {
	return nil, nil
}
func (m *mockLNClient) ListOnchainTransactions(ctx context.Context, from, until, limit, offset uint64) ([]lnclient.OnchainTransaction, error) {
	return nil, nil
}
func (m *mockLNClient) Shutdown() error { return nil }
func (m *mockLNClient) ListChannels(ctx context.Context) ([]lnclient.Channel, error) {
	return nil, nil
}
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
func (m *mockLNClient) DisconnectPeer(ctx context.Context, peerId string) error { return nil }
func (m *mockLNClient) GetNewOnchainAddress(ctx context.Context) (string, error) {
	return "", nil
}
func (m *mockLNClient) ResetRouter(key string) error { return nil }
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
func (m *mockLNClient) GetLogOutput(ctx context.Context, maxLen int) ([]byte, error) {
	return nil, nil
}
func (m *mockLNClient) SignMessage(ctx context.Context, message string) (string, error) {
	return "", nil
}
func (m *mockLNClient) GetStorageDir() (string, error) { return "", nil }
func (m *mockLNClient) GetNetworkGraph(ctx context.Context, nodeIds []string) (lnclient.NetworkGraphResponse, error) {
	return nil, nil
}
func (m *mockLNClient) UpdateLastWalletSyncRequest()                 {}
func (m *mockLNClient) GetSupportedNIP47Methods() []string           { return nil }
func (m *mockLNClient) GetSupportedNIP47NotificationTypes() []string { return nil }
func (m *mockLNClient) GetCustomNodeCommandDefinitions() []lnclient.CustomNodeCommandDef {
	return nil
}
func (m *mockLNClient) ExecuteCustomNodeCommand(ctx context.Context, command *lnclient.CustomNodeCommandRequest) (*lnclient.CustomNodeCommandResponse, error) {
	return nil, nil
}

func (m *mockLNClient) SubscribeChannelAcceptor(ctx context.Context) (<-chan lnclient.ChannelAcceptRequest, func(id string, accept bool, zeroConf bool) error, error) {
	return nil, nil, nil
}

func (m *mockLNClient) SetNodeAlias(ctx context.Context, alias string) error {
	return nil
}

// Tests

func TestLNDTransport_SendCustomMessage(t *testing.T) {
	mock := newMockLNClient()
	transport := NewLNDTransport(mock)

	ctx := context.Background()
	peerPubkey := "03abcdef"
	msgType := uint32(37913)
	data := []byte("test message")

	err := transport.SendCustomMessage(ctx, peerPubkey, msgType, data)
	if err != nil {
		t.Fatalf("SendCustomMessage failed: %v", err)
	}

	if len(mock.sendCalls) != 1 {
		t.Fatalf("Expected 1 send call, got %d", len(mock.sendCalls))
	}

	call := mock.sendCalls[0]
	if call.peerPubkey != peerPubkey {
		t.Errorf("Expected peerPubkey %s, got %s", peerPubkey, call.peerPubkey)
	}
	if call.msgType != msgType {
		t.Errorf("Expected msgType %d, got %d", msgType, call.msgType)
	}
	if string(call.data) != string(data) {
		t.Errorf("Expected data %s, got %s", string(data), string(call.data))
	}
}

func TestLNDTransport_SubscribeCustomMessages(t *testing.T) {
	mock := newMockLNClient()
	transport := NewLNDTransport(mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	msgChan, errChan, err := transport.SubscribeCustomMessages(ctx)
	if err != nil {
		t.Fatalf("SubscribeCustomMessages failed: %v", err)
	}

	// Send test message
	testMsg := lnclient.CustomMessage{
		PeerPubkey: "03xyz",
		Type:       37913,
		Data:       []byte("hello"),
	}
	mock.msgChan <- testMsg

	// Receive message
	select {
	case msg := <-msgChan:
		if msg.PeerPubkey != testMsg.PeerPubkey {
			t.Errorf("Expected peerPubkey %s, got %s", testMsg.PeerPubkey, msg.PeerPubkey)
		}
		if msg.Type != testMsg.Type {
			t.Errorf("Expected type %d, got %d", testMsg.Type, msg.Type)
		}
	case <-errChan:
		t.Fatal("Received error instead of message")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timeout waiting for message")
	}

	cancel()
}
