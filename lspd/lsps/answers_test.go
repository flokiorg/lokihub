package lsps

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/ohstr/nmilat/niplsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func answersHandlers(t *testing.T, jit bool) *Handlers {
	t.Helper()
	cfg := &config.NodeConfig{JitEnabled: jit, MinPaymentSizeMloki: 20_000_000}
	require.NoError(t, cfg.Validate())
	mockNodes := new(MockNodesService)
	mockNodes.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(cfg), Tokens: []string{"tok"}}})
	mockOS := new(MockOpeningService)
	mockOS.On("GetFeeParamsMenu", "tok", mock.Anything).Return([]*core.OpeningFeeParams{{
		MinFeeMloki: 2_000_000, Proportional: 4000, ValidUntil: "2026-10-27T12:00:00.000Z",
		MinLifetime: 43200, MaxClientToSelfDelay: 10080,
		MinPaymentSizeMloki: 20_000_000, MaxPaymentSizeMloki: 10_000_000_000, Promise: "p",
	}}, nil)
	return &Handlers{
		nodesService:   mockNodes,
		openingService: mockOS,
		config:         Config{EnableLsps5Https: true, Lsps5MaxWebhooks: 10},
	}
}

// The handlers' own answers are what the announcement is built from; this
// checks niplsp reads them (the _loki/_mloki dialect) into the announced
// values.
func TestLSPSAnswers_ReadByNiplsp(t *testing.T) {
	a, err := answersHandlers(t, true).LSPSAnswers()
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 5}, a.Protocols)

	lsps1, err := niplsp.ParseLSPS1Info(a.LSPS1Info)
	require.NoError(t, err)
	got := map[string]string{}
	for _, o := range lsps1 {
		got[o.Name] = o.Value
	}
	assert.Equal(t, "20000", got["min_channel_balance"], "loki amounts read as base units")
	assert.Equal(t, "100000000", got["max_channel_balance"])
	assert.Equal(t, "true", got["supports_zero_channel_reserve"])
	assert.Len(t, lsps1, len(niplsp.LSPS1OptionNames()), "every LSPS1 option is given")

	menu, err := niplsp.ParseLSPS2Menu(a.LSPS2Info)
	require.NoError(t, err)
	assert.Equal(t, []niplsp.FeeRow{{
		MinFeeMillis: 2_000_000, Proportional: 4000, MinLifetime: 43200, MaxClientToSelfDelay: 10080,
		MinPaymentSizeMillis: 20_000_000, MaxPaymentSizeMillis: 10_000_000_000,
	}}, menu)

	lsps5, err := niplsp.ParseLSPS5(a.LSPS5)
	require.NoError(t, err)
	assert.Equal(t, []niplsp.Option{{Name: "max_webhooks", Value: "10"}}, lsps5)
}

func TestLSPSAnswers_JitOff(t *testing.T) {
	a, err := answersHandlers(t, false).LSPSAnswers()
	require.NoError(t, err)
	assert.Equal(t, []int{1, 5}, a.Protocols)
	assert.Nil(t, a.LSPS2Info)
	assert.NotNil(t, a.LSPS1Info)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(a.LSPS1Info, &raw))
	assert.Contains(t, raw, "max_channel_balance_loki")
}

// F-L4: a token's menu that fails to read fails the announcement's answers
// (a client's get_info still leaves it out and answers).
func TestLSPSAnswers_FailWhenATokenMenuFails(t *testing.T) {
	cfg := &config.NodeConfig{JitEnabled: true, MinPaymentSizeMloki: 20_000_000}
	require.NoError(t, cfg.Validate())
	mockNodes := new(MockNodesService)
	mockNodes.On("GetNodes").Return([]*core.Node{{NodeConfig: config.NewNodeConfigRef(cfg), Tokens: []string{"ok", "down"}}})
	mockOS := new(MockOpeningService)
	mockOS.On("GetFeeParamsMenu", "ok", mock.Anything).Return([]*core.OpeningFeeParams{{
		MinFeeMloki: 2_000_000, Proportional: 4000, ValidUntil: "2026-10-27T12:00:00.000Z",
		MinLifetime: 43200, MaxClientToSelfDelay: 10080,
		MinPaymentSizeMloki: 20_000_000, MaxPaymentSizeMloki: 10_000_000_000, Promise: "p",
	}}, nil)
	mockOS.On("GetFeeParamsMenu", "down", mock.Anything).Return(([]*core.OpeningFeeParams)(nil), errors.New("db down"))
	h := &Handlers{nodesService: mockNodes, openingService: mockOS}

	_, err := h.LSPSAnswers()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LSPS2")

	resp := h.lsps2GetInfo(&JsonRpcRequest{Id: json.RawMessage(`"id"`)})
	require.Nil(t, resp.Error)
	menu := resp.Result.(map[string]interface{})["opening_fee_params_menu"].([]*Lsps2OpeningFeeParams)
	assert.Len(t, menu, 1, "a client still gets the rows that read")
}
