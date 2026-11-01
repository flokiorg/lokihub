package manager

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestBuyLiquidity_RouteHintUsesKnownPeerAndSurfacesClientTrust is the
// regression test for LOKIHUB-0027 and LOKIHUB-0028: the LSPS2 buy response's
// echoed lsp_node_id is attacker-controlled, so the invoice route-hint hop must
// use the KNOWN peer the Hub bought from (lspPubkey), not that echoed id; and
// the response's client_trusts_lsp flag must be carried through so the per-buy
// trust posture is visible.
func TestBuyLiquidity_RouteHintUsesKnownPeerAndSurfacesClientTrust(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:memdb_lspclientfix?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&persist.LSP{}))
	lspManager := NewLSPManager(db)

	mockLN := &mockLNClientJIT{
		msgChan:  make(chan lnclient.CustomMessage, 10),
		errChan:  make(chan error),
		balances: &lnclient.BalancesResponse{Lightning: lnclient.LightningBalanceResponse{TotalReceivable: 0}},
	}

	cfg := &ManagerConfig{LNClient: mockLN, LSPManager: lspManager}
	m := &LiquidityManager{
		cfg:             cfg,
		transport:       transport.NewFLNDTransport(mockLN),
		eventQueue:      events.NewEventQueue(10),
		listeners:       make(map[string]chan events.Event),
		unclaimedEvents: make(map[string]events.Event),
	}
	m.lsps2Client = lsps2.NewClientHandler(m.transport, m.eventQueue)
	m.lsps0Client = lsps0.NewClientHandler(m.transport, m.eventQueue)
	m.lsps1Client = lsps1.NewClientHandler(m.transport, m.eventQueue)
	m.lsps5Client = lsps5.NewClientHandler(m.transport, m.eventQueue)

	lspPubkey := "03bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	require.NoError(t, m.addTestLSP("TestLSPClientFix", lspPubkey+"@host:5521"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go m.processInternalEvents(ctx)
	go m.processMessages(ctx)

	resultChan := make(chan *JitChannelHints, 1)
	errChan := make(chan error, 1)
	go func() {
		hints, err := m.BuyLiquidity(ctx, lspPubkey, 100000, nil, nil)
		if err != nil {
			errChan <- err
		} else {
			resultChan <- hints
		}
	}()

	waitForMsg := func() *lsps0.JsonRpcRequest {
		for {
			select {
			case err := <-errChan:
				t.Fatalf("BuyLiquidity returned error: %v", err)
				return nil
			case <-time.After(10 * time.Millisecond):
				mockLN.mu.Lock()
				if len(mockLN.sentMsgs) > 0 {
					msg := mockLN.sentMsgs[0]
					mockLN.sentMsgs = mockLN.sentMsgs[1:]
					mockLN.mu.Unlock()
					var req lsps0.JsonRpcRequest
					_ = json.Unmarshal(msg.Data, &req)
					return &req
				}
				mockLN.mu.Unlock()
			case <-ctx.Done():
				return nil
			}
		}
	}

	// 1. GetInfo -> reply with valid params
	req1 := waitForMsg()
	require.NotNil(t, req1, "timeout waiting for GetInfo")
	assert.Equal(t, lsps2.MethodGetInfo, req1.Method)

	paramsMenu := []lsps2.OpeningFeeParams{
		{MinFeeMloki: 100, ValidUntil: time.Now().Add(time.Hour), MinPaymentSizeMloki: 100, MaxPaymentSizeMloki: 10000000},
	}
	resp1 := lsps0.JsonRpcResponse{Jsonrpc: "2.0", ID: req1.ID, Result: lsps2.GetInfoResponse{OpeningFeeParamsMenu: paramsMenu}}
	data1, _ := json.Marshal(resp1)
	mockLN.msgChan <- lnclient.CustomMessage{PeerPubkey: lspPubkey, Type: lsps0.LSPS_MESSAGE_TYPE_ID, Data: data1}

	// 2. Buy -> reply with a DIFFERENT echoed lsp_node_id and client_trusts_lsp=true
	req2 := waitForMsg()
	require.NotNil(t, req2, "timeout waiting for Buy")
	assert.Equal(t, lsps2.MethodBuy, req2.Method)

	evilNodeID := "03cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	buyResult := lsps2.BuyResponse{
		JitChannelSCID:   "800000x1x1",
		LSPNodeID:        evilNodeID, // attacker-controlled echo, must be ignored
		ClientTrustedLSP: true,
	}
	resp2 := lsps0.JsonRpcResponse{Jsonrpc: "2.0", ID: req2.ID, Result: buyResult}
	data2, _ := json.Marshal(resp2)
	mockLN.msgChan <- lnclient.CustomMessage{PeerPubkey: lspPubkey, Type: lsps0.LSPS_MESSAGE_TYPE_ID, Data: data2}

	select {
	case hints := <-resultChan:
		require.NotNil(t, hints)
		assert.Equal(t, lspPubkey, hints.LSPNodeID, "route-hint hop must use the known peer, not the echoed lsp_node_id")
		assert.NotEqual(t, evilNodeID, hints.LSPNodeID, "the attacker-echoed lsp_node_id must not be used")
		assert.True(t, hints.ClientTrustsLSP, "client_trusts_lsp from the buy response must be surfaced")
	case err := <-errChan:
		t.Fatalf("BuyLiquidity failed unexpectedly: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}
}

// reconcileLNClient is a mock LNClient whose ListChannels returns a fixed set,
// for exercising the COMPLETED channel reconciliation.
type reconcileLNClient struct {
	mockLNClient
	channels []lnclient.Channel
}

func (m *reconcileLNClient) ListChannels(ctx context.Context) ([]lnclient.Channel, error) {
	return m.channels, nil
}

func newReconcileManager(t *testing.T, channels []lnclient.Channel) (*LiquidityManager, *LSPManager) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	lspManager := NewLSPManager(db)
	m := &LiquidityManager{
		cfg:          &ManagerConfig{LNClient: &reconcileLNClient{channels: channels}, LSPManager: lspManager},
		nostrPubkeys: map[string]string{},
	}
	return m, lspManager
}

const reconcileLSPNodeID = "03aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// seedPaidOrder creates a PAID order quoting lspBalance loki of inbound.
func seedPaidOrder(t *testing.T, lm *LSPManager, orderID string, lspBalance uint64) {
	t.Helper()
	require.NoError(t, lm.CreateOrder(&persist.LSPS1Order{
		OrderID:    orderID,
		LSPPubkey:  reconcileLSPNodeID,
		State:      "PAID",
		LSPBalance: lspBalance,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}))
}

// LOKIHUB-0026: a COMPLETED must be rejected when the node holds no channel to
// the LSP — the LSP took the fee and opened nothing.
func TestHandleOrderStateUpdate_CompletedRejectedWhenNoChannel(t *testing.T) {
	m, lm := newReconcileManager(t, nil)
	seedPaidOrder(t, lm, "order_no_channel", 100000)

	m.HandleOrderStateUpdate(context.Background(), "order_no_channel", "COMPLETED", reconcileLSPNodeID, "")

	fetched, err := lm.GetOrder("order_no_channel")
	require.NoError(t, err)
	assert.Equal(t, "PAID", fetched.State, "COMPLETED must not be applied without a real channel")
}

// LOKIHUB-0026: a COMPLETED must be rejected when the opened channel is smaller
// than the quoted lsp_balance.
func TestHandleOrderStateUpdate_CompletedRejectedWhenUndersized(t *testing.T) {
	// Quote 100000 loki of inbound; open a channel of only 50000 loki (in mloki).
	channels := []lnclient.Channel{{
		RemotePubkey:  reconcileLSPNodeID,
		LocalBalance:  0,
		RemoteBalance: 50000 * 1000,
	}}
	m, lm := newReconcileManager(t, channels)
	seedPaidOrder(t, lm, "order_undersized", 100000)

	m.HandleOrderStateUpdate(context.Background(), "order_undersized", "COMPLETED", reconcileLSPNodeID, "")

	fetched, err := lm.GetOrder("order_undersized")
	require.NoError(t, err)
	assert.Equal(t, "PAID", fetched.State, "COMPLETED must not be applied for an undersized channel")
}

// LOKIHUB-0026: a COMPLETED is applied once the node confirms a channel to the
// LSP of at least the quoted capacity.
func TestHandleOrderStateUpdate_CompletedAppliedWhenChannelMatches(t *testing.T) {
	channels := []lnclient.Channel{{
		RemotePubkey:  reconcileLSPNodeID,
		LocalBalance:  0,
		RemoteBalance: 100000 * 1000,
	}}
	m, lm := newReconcileManager(t, channels)
	seedPaidOrder(t, lm, "order_ok", 100000)

	m.HandleOrderStateUpdate(context.Background(), "order_ok", "COMPLETED", reconcileLSPNodeID, "")

	fetched, err := lm.GetOrder("order_ok")
	require.NoError(t, err)
	assert.Equal(t, "COMPLETED", fetched.State, "COMPLETED must be applied once a sufficient channel exists")
}

// LOKIHUB-0026: when the LSP reports a funding outpoint it must match a real
// channel; a non-matching outpoint is rejected even if another channel exists.
func TestHandleOrderStateUpdate_CompletedRequiresMatchingFundingOutpoint(t *testing.T) {
	channels := []lnclient.Channel{{
		RemotePubkey:  reconcileLSPNodeID,
		LocalBalance:  0,
		RemoteBalance: 100000 * 1000,
		FundingTxId:   "deadbeef",
		FundingTxVout: 0,
	}}

	// Wrong outpoint -> rejected.
	m1, lm1 := newReconcileManager(t, channels)
	seedPaidOrder(t, lm1, "order_wrong_outpoint", 100000)
	m1.HandleOrderStateUpdate(context.Background(), "order_wrong_outpoint", "COMPLETED", reconcileLSPNodeID, "feedface:1")
	f1, err := lm1.GetOrder("order_wrong_outpoint")
	require.NoError(t, err)
	assert.Equal(t, "PAID", f1.State, "a funding outpoint that matches no channel must be rejected")

	// Matching outpoint -> applied.
	m2, lm2 := newReconcileManager(t, channels)
	seedPaidOrder(t, lm2, "order_right_outpoint", 100000)
	m2.HandleOrderStateUpdate(context.Background(), "order_right_outpoint", "COMPLETED", reconcileLSPNodeID, "deadbeef:0")
	f2, err := lm2.GetOrder("order_right_outpoint")
	require.NoError(t, err)
	assert.Equal(t, "COMPLETED", f2.State, "a matching funding outpoint must be accepted")
}

// LOKIHUB-0026 (dead Nostr path): a notification keyed by the LSP's x-only
// Nostr pubkey must resolve to the owning LSP's node id and apply — while an
// unrelated key must still be rejected (the ownership guard is mapped, never
// weakened).
func TestHandleOrderStateUpdate_NostrKeyResolvesToNodeID(t *testing.T) {
	m, lm := newReconcileManager(t, nil)
	_, err := lm.addTestLSP("NostrLSP", reconcileLSPNodeID, "host:9735", true)
	require.NoError(t, err)

	nostrKey := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" //nolint:gosec // test-only x-only key
	require.NoError(t, lm.UpdateLSPNostrPubkey(reconcileLSPNodeID, nostrKey))

	require.NoError(t, lm.CreateOrder(&persist.LSPS1Order{
		OrderID: "order_nostr", LSPPubkey: reconcileLSPNodeID, State: "CREATED",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	// PAID needs no channel check; the only thing under test is the key mapping.
	m.HandleOrderStateUpdate(context.Background(), "order_nostr", "PAID", nostrKey, "")
	f, err := lm.GetOrder("order_nostr")
	require.NoError(t, err)
	assert.Equal(t, "PAID", f.State, "a Nostr-key-addressed update must resolve to the node id and apply")

	// An unrelated key must not change the order.
	strangerKey := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" //nolint:gosec // test-only x-only key
	m.HandleOrderStateUpdate(context.Background(), "order_nostr", "CANCELLED", strangerKey, "")
	f2, err := lm.GetOrder("order_nostr")
	require.NoError(t, err)
	assert.Equal(t, "PAID", f2.State, "an unknown key must not change the order (ownership guard intact)")
}

// LOKIHUB-0026: an unknown order_state is ignored, never stored.
func TestHandleOrderStateUpdate_UnknownStateIgnored(t *testing.T) {
	m, lm := newReconcileManager(t, nil)
	seedPaidOrder(t, lm, "order_bogus_state", 100000)

	m.HandleOrderStateUpdate(context.Background(), "order_bogus_state", "TOTALLY_BOGUS", reconcileLSPNodeID, "")

	fetched, err := lm.GetOrder("order_bogus_state")
	require.NoError(t, err)
	assert.Equal(t, "PAID", fetched.State, "an unknown order state must be ignored, not stored")
}
