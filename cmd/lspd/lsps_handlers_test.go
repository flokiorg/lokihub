package main

import (
	"testing"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/ohstr/nmilat/niplsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// menuOpeningService gives each token a menu whose min fee tells them apart.
type menuOpeningService struct {
	ports.OpeningService
	minFee map[string]uint64
}

func (m menuOpeningService) GetFeeParamsMenu(token string, _ *btcec.PrivateKey) ([]*core.OpeningFeeParams, error) {
	return []*core.OpeningFeeParams{{
		MinFeeMloki: m.minFee[token], Proportional: 4000, ValidUntil: "2026-10-27T12:00:00.000Z",
		MinLifetime: 43200, MaxClientToSelfDelay: 10080,
		MinPaymentSizeMloki: 20_000_000, MaxPaymentSizeMloki: 10_000_000_000, Promise: "p",
	}}, nil
}

func fleetNode(t *testing.T, jit bool, maxChannelLoki uint64, token string) *core.Node {
	t.Helper()
	cfg := &config.NodeConfig{Name: token, JitEnabled: jit, MinPaymentSizeMloki: 20_000_000, Lsps1MaxChannelBalanceLoki: &maxChannelLoki}
	require.NoError(t, cfg.Validate())
	return &core.Node{NodeConfig: config.NewNodeConfigRef(cfg), Tokens: []string{token}}
}

func lsps1Option(t *testing.T, a *ports.LSPSAnswers, name string) string {
	t.Helper()
	opts, err := niplsp.ParseLSPS1Info(a.LSPS1Info)
	require.NoError(t, err)
	for _, o := range opts {
		if o.Name == name {
			return o.Value
		}
	}
	t.Fatalf("no LSPS1 option %s", name)
	return ""
}

// In a fleet each node answers, and so announces, its own protocol list,
// JIT switch, LSPS1 options and fee menu, not the first node's.
func TestNodeLSPSHandlers_EachNodeAnswersFromItsOwnConfig(t *testing.T) {
	first := fleetNode(t, false, 1_000_000, "first")
	second := fleetNode(t, true, 5_000_000, "second")
	os := menuOpeningService{minFee: map[string]uint64{"first": 1_000_000, "second": 2_000_000}}
	cfg := lsps.Config{EnableLsps5Https: true, Lsps5MaxWebhooks: 10}

	h1, err := newNodeLSPSHandlers(first, cfg, nil, os, nil, nil, nil)
	require.NoError(t, err)
	h2, err := newNodeLSPSHandlers(second, cfg, nil, os, nil, nil, nil)
	require.NoError(t, err)

	a1, err := h1.LSPSAnswers()
	require.NoError(t, err)
	a2, err := h2.LSPSAnswers()
	require.NoError(t, err)

	assert.Equal(t, []int{1, 5}, a1.Protocols, "first node: JIT off")
	assert.Equal(t, []int{1, 2, 5}, a2.Protocols, "second node: JIT on")
	assert.Nil(t, a1.LSPS2Info)
	assert.Equal(t, "1000000", lsps1Option(t, a1, "max_channel_balance"))
	assert.Equal(t, "5000000", lsps1Option(t, a2, "max_channel_balance"))

	menu, err := niplsp.ParseLSPS2Menu(a2.LSPS2Info)
	require.NoError(t, err)
	require.Len(t, menu, 1, "only the second node's own token menu")
	assert.Equal(t, uint64(2_000_000), menu[0].MinFeeMillis)
}

// A single node still answers exactly as before.
func TestNodeLSPSHandlers_SingleNodeUnchanged(t *testing.T) {
	only := fleetNode(t, true, 1_000_000, "only")
	os := menuOpeningService{minFee: map[string]uint64{"only": 1_000_000}}
	h, err := newNodeLSPSHandlers(only, lsps.Config{}, nil, os, nil, nil, nil)
	require.NoError(t, err)

	a, err := h.LSPSAnswers()
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, a.Protocols)
	menu, err := niplsp.ParseLSPS2Menu(a.LSPS2Info)
	require.NoError(t, err)
	assert.Len(t, menu, 1)
}
