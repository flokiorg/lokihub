package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockTransport allows capturing messages
type MockTransport struct {
	mock.Mock
	CustomHandler func(ctx context.Context, peerPubkey string, msgType uint32, data []byte)
}

func (m *MockTransport) SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error {
	if m.CustomHandler != nil {
		m.CustomHandler(ctx, peerPubkey, msgType, data)
	}
	args := m.Called(ctx, peerPubkey, msgType, data)
	return args.Error(0)
}

func (m *MockTransport) SubscribeCustomMessages(ctx context.Context) (<-chan transport.CustomMessage, <-chan error, error) {
	return nil, nil, nil
}

// TestJitRetryOnExpiredParams verifies retry logic for both Code 201 (BLIP) and 100 (flspd)
func TestJitRetryOnExpiredParams(t *testing.T) {
	// Parse time
	validUntil, _ := time.Parse(time.RFC3339, "2050-01-01T00:00:00Z")

	// Mock valid fee params
	_ = lsps2.OpeningFeeParams{
		MinFeeMloki:         100,
		Proportional:        100,
		ValidUntil:          validUntil,
		MinPaymentSizeMloki: 1000,
		MaxPaymentSizeMloki: 1000000,
		Promise:             "test_promise",
	}

	tests := []struct {
		name      string
		errorCode int
		errorMsg  string
	}{
		{
			name:      "Standard Stale Params (201)",
			errorCode: 201,
			errorMsg:  "invalid_opening_fee_params",
		},
		{
			name:      "Flspd Stale Params (100)",
			errorCode: 100,
			errorMsg:  "Invalid promise or expired params",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fresh mocks for each subtest
			mockLN := &MockLNClient{}
			mockTransport := &MockTransport{}
			eq := events.NewEventQueue(10)

			m := &LiquidityManager{
				cfg: &ManagerConfig{
					LNClient: mockLN,
				},
				transport:       mockTransport,
				eventQueue:      eq,
				listeners:       make(map[string]chan events.Event),
				unclaimedEvents: make(map[string]events.Event),
				lsps2Client:     lsps2.NewClientHandler(mockTransport, eq), // Use real client with mock transport
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			// We launch a routine to feed expected events into the listener/queue when it sees a request
			// This is tricky without fully mocking the ClientHandler which is struct.
			// Instead we can intercept the "waitForEvent" by populating `unclaimedEvents` immediately after call?
			// No, `waitForEvent` creates a channel.

			// We can intercept the Transport Send calls and inject the Response Event into the queue.
			// But specific RequestIDs are generated inside.
			// Actually ClientHandler generates RequestID, sends message, then waits.
			// We can just bypass ClientHandler for this test and inject Mock Events directly into `unclaimedEvents`
			// IF we mock the Client methods.
			// But `LiquidityManager` uses struct fields `lsps2Client`. We can't swap those easily without interface.

			// OK, we must use the Real `lsps2Client` + Mock `transport`.
			// When `RequestOpeningParams` calls `transport.SendCustomMessage`, we catch it.
			// In the mock callback, we can parse the request, extract ID, and feed a RESPONSE back via `HandleMessage`.

			// Counter for attempts to verify retry
			attempts := 0

			// Use .Run() to capture calls instead of CustomHandler
			mockTransport.On("SendCustomMessage", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				peer := args.Get(1).(string)
				// msgType := args.Get(2).(uint32)
				data := args.Get(3).([]byte)

				if peer != "lsp_pubkey" {
					return
				}

				id := extractIdFromBytes(data)
				method := extractMethodFromBytes(data)

				if method == "lsps2.get_info" {
					// Respond with Fee Params
					go func() {
						resp := fmt.Sprintf(`{"jsonrpc":"2.0", "id":"%s", "result": {"opening_fee_params_menu": [{"min_fee_mloki": "100", "proportional": 100, "valid_until": "2099-01-01T00:00:00Z", "min_payment_size_mloki": "1000", "max_payment_size_mloki": "1000000", "promise": "fake_promise"}]}}`, id)
						_ = m.lsps2Client.HandleMessage("lsp_pubkey", []byte(resp))
					}()
				} else if method == "lsps2.buy" {
					attempts++
					go func() {
						// First attempt fails with specific code
						if attempts == 1 {
							errResp := fmt.Sprintf(`{"jsonrpc":"2.0", "id":"%s", "error": {"code": %d, "message": "%s"}}`, id, tt.errorCode, tt.errorMsg)
							_ = m.lsps2Client.HandleMessage("lsp_pubkey", []byte(errResp))
						} else {
							// Second attempt succeeds
							succResp := fmt.Sprintf(`{"jsonrpc":"2.0", "id":"%s", "result": {"jit_channel_scid": "123x456x789", "lsp_cltv_expiry_delta": 144, "client_trusts_lsp": false, "lsp_node_id": "lsp_node_id"}}`, id)
							_ = m.lsps2Client.HandleMessage("lsp_pubkey", []byte(succResp))
							// Don't close done - it can panic if called twice
						}
					}()
				}
			}).Return(nil)

			// Start event processing loop
			go m.ProcessInternalEventsForTest(ctx)

			// Execute
			hints, err := m.BuyLiquidity(ctx, "lsp_pubkey", 100000, nil)

			assert.NoError(t, err)
			if hints != nil {
				// SCID is parsed from "123x456x789" -> encoded uint64 -> formatted string
				assert.NotEmpty(t, hints.SCID)
			}
			assert.Equal(t, 2, attempts, "Should have retried once")
		})
	}
}

// TestLSPS1ErrorPropagation verifies parsing of error codes
func TestLSPS1ErrorPropagation(t *testing.T) {
	// Setup generic
	mockLN := &MockLNClient{}
	mockTransport := &MockTransport{}
	eq := events.NewEventQueue(10)

	m := &LiquidityManager{
		cfg:             &ManagerConfig{LNClient: mockLN},
		transport:       mockTransport,
		eventQueue:      eq,
		listeners:       make(map[string]chan events.Event),
		unclaimedEvents: make(map[string]events.Event),
		lsps1Client:     lsps1.NewClientHandler(mockTransport, eq),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mockTransport.On("SendCustomMessage", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		data := args.Get(3).([]byte)
		id := extractIdFromBytes(data)

		go func() {
			// Reply with flspd Code 100 error
			errResp := fmt.Sprintf(`{"jsonrpc":"2.0", "id":"%s", "error": {"code": 100, "message": "Client balance out of bounds", "data": {"min": 0, "max": 100}} }`, id)
			m.lsps1Client.HandleMessage("lsp_pubkey", []byte(errResp))
		}()
	}).Return(nil)

	// Start event processing loop
	go m.ProcessInternalEventsForTest(ctx)

	// We test GetLSPS1InfoList but we expect it to fail
	_, err := m.GetLSPS1InfoList(ctx, "lsp_pubkey")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Client balance out of bounds")
}

// Helpers
func extractIdFromBytes(data []byte) string {
	// Quick hacky json parse
	// In real test use struct
	var req struct {
		ID string `json:"id"`
	}
	json.Unmarshal(data, &req) // ignore err
	return req.ID
}

func extractMethodFromBytes(data []byte) string {
	var req struct {
		Method string `json:"method"`
	}
	json.Unmarshal(data, &req)
	return req.Method
}

// MockLNClient stub
type MockLNClient struct {
	lnclient.LNClient
	mock.Mock
}

func (m *MockLNClient) ConnectPeer(ctx context.Context, req *lnclient.ConnectPeerRequest) error {
	return nil
}
func (m *MockLNClient) GetBalances(ctx context.Context, onchain bool) (*lnclient.BalancesResponse, error) {
	return nil, nil
}
func (m *MockLNClient) SubscribeCustomMessages(ctx context.Context) (<-chan lnclient.CustomMessage, <-chan error, error) {
	return nil, nil, nil
}
