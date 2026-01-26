package manager

import (
	"context"
	"encoding/json"
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

func TestBuyLiquidity_RetryOnStaleParams(t *testing.T) {
	// Setup DB
	db, _ := gorm.Open(sqlite.Open("file:memdb_manual?mode=memory&cache=shared"), &gorm.Config{})
	db.AutoMigrate(&persist.LSP{})
	lspManager := NewLSPManager(db)

	mockLN := &mockLNClientJIT{
		msgChan:  make(chan lnclient.CustomMessage, 10),
		errChan:  make(chan error),
		balances: &lnclient.BalancesResponse{Lightning: lnclient.LightningBalanceResponse{TotalReceivable: 0}},
	}

	cfg := &ManagerConfig{LNClient: mockLN, LSPManager: lspManager}
	m := &LiquidityManager{
		cfg:             cfg,
		transport:       transport.NewLNDTransport(mockLN),
		eventQueue:      events.NewEventQueue(10),
		listeners:       make(map[string]chan events.Event),
		unclaimedEvents: make(map[string]events.Event),
	}
	m.lsps2Client = lsps2.NewClientHandler(m.transport, m.eventQueue)
	m.lsps0Client = lsps0.NewClientHandler(m.transport, m.eventQueue)
	m.lsps1Client = lsps1.NewClientHandler(m.transport, m.eventQueue)
	m.lsps5Client = lsps5.NewClientHandler(m.transport, m.eventQueue)

	// Add Active LSP
	lspPubkey := "03bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	err := m.AddLSP("TestLSPManual", lspPubkey+"@host:9735")
	assert.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go m.processInternalEvents(ctx)
	msgChan, errChanMock, _ := mockLN.SubscribeCustomMessages(ctx)
	go m.processMessages(ctx, msgChan, errChanMock)

	resultChan := make(chan *JitChannelHints, 1)
	errChan := make(chan error, 1)

	go func() {
		// Call BuyLiquidity explicitly
		hints, err := m.BuyLiquidity(ctx, "lsp1", 100000, nil)
		if err != nil {
			errChan <- err
		} else {
			resultChan <- hints
		}
	}()

	waitForMsg := func() (lnclient.CustomMessage, *lsps0.JsonRpcRequest) {
		for {
			select {
			case err := <-errChan:
				t.Fatalf("BuyLiquidity returned error: %v", err)
				return lnclient.CustomMessage{}, nil
			case <-time.After(10 * time.Millisecond):
				mockLN.mu.Lock()
				if len(mockLN.sentMsgs) > 0 {
					msg := mockLN.sentMsgs[0]
					mockLN.sentMsgs = mockLN.sentMsgs[1:]
					mockLN.mu.Unlock()
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

	// Reply with ERROR 201
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
	resp3 := maxCopy(resp1)
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
	// 800000x1x1 (block 800000, tx 1, output 1)
	// (800000 << 40) | (1 << 16) | 1 = 8796093022208 + 65536 + 1 = 8796093087745
	buyResult := lsps2.BuyResponse{
		JitChannelSCID: "800000x1x1",
		LSPNodeID:      lspPubkey,
	}
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
		// Expected uint64 scid string
		expectedSCID := "879609302220865537"
		assert.Equal(t, expectedSCID, hints.SCID)
		assert.Equal(t, lspPubkey, hints.LSPNodeID)
		t.Log("Successfully handled Manual Buy retry sequence!")
	case err := <-errChan:
		t.Fatalf("BuyLiquidity failed unexpectedly: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for result")
	}
}
