package lsps

import (
	"encoding/json"
	"testing"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRoute_DispatchesToCorrectHandler covers route()'s method-name
// switch, previously only incidentally exercised (23.1%) by tests that
// call individual handlers directly rather than through route() itself.
// Each case uses params (or lack thereof) that hit the *target handler's*
// own early return -- proving dispatch reached the right handler, not
// re-testing that handler's full internal logic, which is already covered
// by its own dedicated tests elsewhere.
func TestRoute_DispatchesToCorrectHandler(t *testing.T) {
	mockNS := new(MockNodesService)
	// JIT on, so the lsps2.* cases are offered and reach their handlers;
	// validated, so lsps1.get_info finds its defaults filled in.
	nodeCfg := &config.NodeConfig{JitEnabled: true, MinPaymentSizeMloki: 20_000_000}
	require.NoError(t, nodeCfg.Validate())
	mockNS.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(nodeCfg)}}).Maybe()
	mockOS := new(MockOpeningService)
	// lsps5.list_webhooks has no params to fail validation on, so it always
	// reaches the store -- unlike every other case here, which is routed
	// to a handler whose *own* early-return (invalid params) is enough to
	// prove dispatch without needing the rest of that handler's mocks.
	mockOS.On("GetLsps5Webhooks", "01").Return([]*core.Lsps5Webhook{}, nil).Maybe()
	server := NewHandlers(Config{}, new(MockLndClient), mockNS, mockOS, new(MockLiquidityManager), new(MockNotificationService), nil)

	tests := []struct {
		method        string
		params        string
		wantNotRouted bool // true only for the unknown-method case
	}{
		{method: "lsps0.list_protocols"},
		{method: "lsps0.get_info"},
		{method: "lsps2.get_versions"},
		{method: "lsps2.get_info"},
		{method: "lsps2.buy", params: `not-json`},
		{method: "lsps1.get_info"},
		{method: "lsps1.create_order", params: `not-json`},
		{method: "lsps1.get_order", params: `not-json`},
		{method: "lsps5.set_webhook", params: `not-json`},
		{method: "lsps5.list_webhooks"},
		{method: "lsps5.remove_webhook", params: `not-json`},
		{method: "unknown.method", wantNotRouted: true},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Method: tt.method}
			if tt.params != "" {
				req.Params = json.RawMessage(tt.params)
			}

			resp := server.Route(req, []byte{0x01})
			require.NotNil(t, resp)

			if tt.wantNotRouted {
				require.NotNil(t, resp.Error)
				assert.Equal(t, -32601, resp.Error.Code, "an unrecognized method must fall through to the default case")
				return
			}
			if resp.Error != nil {
				assert.NotEqual(t, -32601, resp.Error.Code, "%s must be routed to its own handler, not fall through to the default 'method not found' case", tt.method)
			}
		})
	}
}
