package lsps

import (
	"encoding/json"
	"testing"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportedProtocols(t *testing.T) {
	jitOn := &config.NodeConfig{JitEnabled: true}
	jitOff := &config.NodeConfig{JitEnabled: false}
	https := Config{EnableLsps5Https: true}
	nostr := Config{EnableLsps5Nostr: true}

	cases := []struct {
		name string
		cfg  *config.NodeConfig
		c    Config
		want []int
	}{
		{"all on", jitOn, Config{EnableLsps5Https: true, EnableLsps5Nostr: true}, []int{1, 2, 5}},
		{"jit off drops 2", jitOff, https, []int{1, 5}},
		{"nostr only keeps 5", jitOn, nostr, []int{1, 2, 5}},
		{"no lsps5 transport drops 5", jitOn, Config{}, []int{1, 2}},
		{"only lsps1", jitOff, Config{}, []int{1}},
		{"no node config is jit off", nil, https, []int{1, 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SupportedProtocols(tc.cfg, tc.c))
		})
	}
}

func listProtocols(t *testing.T, server *Handlers) []int {
	t.Helper()
	req := &JsonRpcRequest{Id: json.RawMessage(`"list-protocols-id"`), Method: "lsps0.list_protocols"}
	resp := server.handleLsps0ListProtocols(req)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)
	result, ok := resp.Result.(map[string][]int)
	require.True(t, ok)
	return result["protocols"]
}

func TestHandleLsps0ListProtocols(t *testing.T) {
	ref := config.NewNodeConfigRef(&config.NodeConfig{JitEnabled: true})
	mockNodes := new(MockNodesService)
	mockNodes.On("GetNodes").Return([]*core.Node{{NodeConfig: ref}})
	server := &Handlers{nodesService: mockNodes, config: Config{EnableLsps5Https: true}}

	assert.Equal(t, []int{1, 2, 5}, listProtocols(t, server))

	// A hot-swap turning JIT off is seen on the next call.
	ref.Store(&config.NodeConfig{JitEnabled: false})
	assert.Equal(t, []int{1, 5}, listProtocols(t, server))
	assert.Equal(t, server.SupportedProtocols(), listProtocols(t, server))
}

func TestHandleLsps0ListProtocols_NoNodes(t *testing.T) {
	assert.Equal(t, []int{1}, listProtocols(t, &Handlers{}))
}

func TestHandleLsps0GetInfo_WithoutNostrPubkey(t *testing.T) {
	server := &Handlers{config: Config{}}
	req := &JsonRpcRequest{Id: json.RawMessage(`"get-info-id"`), Method: "lsps0.get_info"}

	resp := server.handleLsps0GetInfo(req)

	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)
	result, ok := resp.Result.(Lsps0GetInfoResponse)
	require.True(t, ok)
	assert.Equal(t, []int{1, 2, 5}, result.SupportedVersions)
	assert.Empty(t, result.NotificationNostrPubkey, "must omit the nostr pubkey field when none is configured")
}

func TestHandleLsps0GetInfo_WithNostrPubkey(t *testing.T) {
	server := &Handlers{config: Config{Lsps5NostrPubkey: "deadbeef"}}
	req := &JsonRpcRequest{Id: json.RawMessage(`"get-info-id"`), Method: "lsps0.get_info"}

	resp := server.handleLsps0GetInfo(req)

	require.NotNil(t, resp)
	result, ok := resp.Result.(Lsps0GetInfoResponse)
	require.True(t, ok)
	assert.Equal(t, "deadbeef", result.NotificationNostrPubkey)
}

// TestRoute_Lsps2RefusedWhileJitOff: a protocol not in list_protocols is
// not served, so a client's live check matches the announced protocols.
func TestRoute_Lsps2RefusedWhileJitOff(t *testing.T) {
	ref := config.NewNodeConfigRef(&config.NodeConfig{JitEnabled: false})
	mockNodes := new(MockNodesService)
	mockNodes.On("GetNodes").Return([]*core.Node{{NodeConfig: ref}})
	server := &Handlers{nodesService: mockNodes, config: Config{EnableLsps5Https: true}}

	for _, method := range []string{"lsps2.get_info", "lsps2.buy", "lsps2.get_versions"} {
		resp := server.Route(&JsonRpcRequest{Id: json.RawMessage(`1`), Method: method, Params: json.RawMessage(`{}`)}, []byte{1})
		require.NotNil(t, resp.Error, method)
		assert.Equal(t, -32601, resp.Error.Code, method)
	}

	// Turning JIT on serves LSPS2 again (get_versions needs no other mocks).
	ref.Store(&config.NodeConfig{JitEnabled: true})
	resp := server.Route(&JsonRpcRequest{Id: json.RawMessage(`1`), Method: "lsps2.get_versions"}, []byte{1})
	assert.Nil(t, resp.Error)
}
