package lsps

import (
	"encoding/json"
	"testing"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleLsps2GetVersions(t *testing.T) {
	server := &Handlers{}
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`)}

	resp := server.handleLsps2GetVersions(req)

	require.NotNil(t, resp)
	result, ok := resp.Result.(map[string][]int)
	require.True(t, ok)
	assert.Equal(t, []int{1}, result["versions"])
}

func TestLsps2GetInfo_AggregatesMenuAcrossAllNodesAndTokens(t *testing.T) {
	mockNS := new(MockNodesService)
	mockOS := new(MockOpeningService)

	nodeA := &core.Node{Tokens: []string{"token-a"}}
	nodeB := &core.Node{Tokens: []string{"token-b"}}
	mockNS.On("GetNodes").Return([]*core.Node{nodeA, nodeB})
	mockOS.On("GetFeeParamsMenu", "token-a", nodeA.PrivateKey).Return([]*core.OpeningFeeParams{
		{MinFeeMloki: 1000, Proportional: 100, ValidUntil: "2099-01-01T00:00:00.000Z"},
	}, nil)
	mockOS.On("GetFeeParamsMenu", "token-b", nodeB.PrivateKey).Return([]*core.OpeningFeeParams{
		{MinFeeMloki: 2000, Proportional: 200, ValidUntil: "2099-01-01T00:00:00.000Z"},
	}, nil)

	server := NewHandlers(Config{}, new(MockLndClient), mockNS, mockOS, new(MockLiquidityManager), new(MockNotificationService), nil)
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`)}

	resp := server.lsps2GetInfo(req)

	result, ok := resp.Result.(map[string]interface{})
	require.True(t, ok)
	menu, ok := result["opening_fee_params_menu"].([]*Lsps2OpeningFeeParams)
	require.True(t, ok)
	require.Len(t, menu, 2, "must aggregate fee params from every node/token, not just the first")
}

func TestLsps2GetInfo_FiltersByRequestedToken(t *testing.T) {
	mockNS := new(MockNodesService)
	mockOS := new(MockOpeningService)

	nodeA := &core.Node{Tokens: []string{"token-a"}}
	nodeB := &core.Node{Tokens: []string{"token-b"}}
	mockNS.On("GetNodes").Return([]*core.Node{nodeA, nodeB})
	mockOS.On("GetFeeParamsMenu", "token-a", nodeA.PrivateKey).Return([]*core.OpeningFeeParams{
		{MinFeeMloki: 1000, Proportional: 100, ValidUntil: "2099-01-01T00:00:00.000Z"},
	}, nil)

	server := NewHandlers(Config{}, new(MockLndClient), mockNS, mockOS, new(MockLiquidityManager), new(MockNotificationService), nil)
	params, err := json.Marshal(Lsps2GetInfoLegacyParams{Token: "token-a"})
	require.NoError(t, err)
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: params}

	resp := server.lsps2GetInfo(req)

	result, ok := resp.Result.(map[string]interface{})
	require.True(t, ok)
	menu, ok := result["opening_fee_params_menu"].([]*Lsps2OpeningFeeParams)
	require.True(t, ok)
	require.Len(t, menu, 1, "a node whose Tokens doesn't include the requested token must be skipped entirely")
	mockOS.AssertNotCalled(t, "GetFeeParamsMenu", "token-b", nodeB.PrivateKey)
}

// TestLsps2GetInfo_FeeParamsErrorIsSkippedNotFatal covers the per-token
// error-tolerance branch: one node/token's GetFeeParamsMenu failing must
// not abort the whole aggregation, just omit that entry.
func TestLsps2GetInfo_FeeParamsErrorIsSkippedNotFatal(t *testing.T) {
	mockNS := new(MockNodesService)
	mockOS := new(MockOpeningService)

	nodeA := &core.Node{Tokens: []string{"token-a"}}
	nodeB := &core.Node{Tokens: []string{"token-b"}}
	mockNS.On("GetNodes").Return([]*core.Node{nodeA, nodeB})
	mockOS.On("GetFeeParamsMenu", "token-a", nodeA.PrivateKey).Return([]*core.OpeningFeeParams{}, assert.AnError)
	mockOS.On("GetFeeParamsMenu", "token-b", nodeB.PrivateKey).Return([]*core.OpeningFeeParams{
		{MinFeeMloki: 2000, Proportional: 200, ValidUntil: "2099-01-01T00:00:00.000Z"},
	}, nil)

	server := NewHandlers(Config{}, new(MockLndClient), mockNS, mockOS, new(MockLiquidityManager), new(MockNotificationService), nil)
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`)}

	resp := server.lsps2GetInfo(req)

	result, ok := resp.Result.(map[string]interface{})
	require.True(t, ok)
	menu, ok := result["opening_fee_params_menu"].([]*Lsps2OpeningFeeParams)
	require.True(t, ok)
	require.Len(t, menu, 1, "the failing node's fee params must be omitted, not abort the whole response")
	assert.Equal(t, "2000", menu[0].MinFeeMloki)
}

func TestLsps2GetInfo_EmptyParamsIsTolerated(t *testing.T) {
	mockNS := new(MockNodesService)
	mockOS := new(MockOpeningService)
	mockNS.On("GetNodes").Return([]*core.Node{})

	server := NewHandlers(Config{}, new(MockLndClient), mockNS, mockOS, new(MockLiquidityManager), new(MockNotificationService), nil)
	req := &JsonRpcRequest{Id: json.RawMessage(`"id"`), Params: nil}

	resp := server.lsps2GetInfo(req)
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
}
