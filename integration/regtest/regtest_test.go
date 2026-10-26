package regtest

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGuardsRefuseEveryOtherNetwork(t *testing.T) {
	require.NoError(t, CheckChainName("regtest"))
	for _, c := range []string{"main", "mainnet", "test", "testnet4", "signet", "simnet", ""} {
		require.True(t, errors.Is(CheckChainName(c), ErrNotRegtest), c)
	}

	ok := &lnrpc.GetInfoResponse{Chains: []*lnrpc.Chain{{Chain: "flokicoin", Network: "regtest"}}}
	require.NoError(t, CheckNodeNetwork("hub", ok))
	for _, n := range []string{"mainnet", "testnet", "signet", "simnet"} {
		info := &lnrpc.GetInfoResponse{Chains: []*lnrpc.Chain{{Chain: "flokicoin", Network: n}}}
		require.True(t, errors.Is(CheckNodeNetwork("hub", info), ErrNotRegtest), n)
	}
	require.True(t, errors.Is(CheckNodeNetwork("hub", &lnrpc.GetInfoResponse{}), ErrNotRegtest))
	require.True(t, errors.Is(CheckNodeNetwork("hub", nil), ErrNotRegtest))
}

func TestFundingPlan(t *testing.T) {
	require.Equal(t, []MineStep{{"payer", 100}, {"hub", 101}, {"payer", 10}}, FundingPlan(1))
	require.Equal(t, []MineStep{{"hub", 51}, {"payer", 10}}, FundingPlan(151))
	require.Equal(t, []MineStep{{"payer", 2}}, FundingPlan(210))
	require.Empty(t, FundingPlan(212), "a funded chain mines nothing more")
	require.Empty(t, FundingPlan(5000))
}

func TestBlockTimeRisesStrictly(t *testing.T) {
	now := time.Unix(1_700_000_000, 500)
	require.Equal(t, time.Unix(1_700_000_000, 0), BlockTime(now.Add(-time.Hour), now))
	prev := time.Unix(1_700_000_000, 0)
	require.Equal(t, prev.Add(time.Second), BlockTime(prev, now), "same second as the tip")
	ahead := now.Add(time.Minute)
	require.Equal(t, ahead.Add(time.Second), BlockTime(ahead, now), "a burst runs ahead of the clock")
}

func TestBlockVersionStampsChainIDFromAuxpowHeight(t *testing.T) {
	const tmpl = 0x20000002
	require.Equal(t, int32(tmpl), BlockVersion(tmpl, Params.AuxpowHeightEffective-1))
	v := BlockVersion(tmpl, Params.AuxpowHeightEffective)
	h := wire.BlockHeader{Version: v}
	require.Equal(t, Params.AuxpowChainId, h.GetChainID())
	require.Equal(t, int32(tmpl), v&^wire.ChainIDMask, "the version bits are kept")
}

func TestOrderMempool(t *testing.T) {
	deps := map[string][]string{
		"c": {"b"},
		"b": {"a"},
		"a": nil,
		"d": nil,
		"e": {"d", "outside"}, // a parent already mined is not in the pool
	}
	require.Equal(t, []string{"a", "b", "c", "d", "e"}, OrderMempool(deps, nil))
	require.Equal(t, []string{"d", "e"}, OrderMempool(deps, map[string]bool{"a": true}), "a skipped tx takes its descendants with it")
	require.Equal(t, []string{"a", "b", "c"}, OrderMempool(deps, map[string]bool{"d": true}))
}

func TestMacaroonPathIsRegtest(t *testing.T) {
	require.Equal(t, filepath.Join("x", "data", "chain", "flokicoin", "regtest", "admin.macaroon"), MacaroonPath("x"))
}

func TestWriteConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.local.yaml")
	var c IntegrationConfig
	c.AdminAPI.BaseURL = "http://localhost:5610"
	c.AdminAPI.Token = "tok"
	c.RelayURL = "ws://relay:5500"
	c.Regtest.ChainRPC = "localhost:25213"
	require.NoError(t, WriteConfig(path, c))

	st, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var back IntegrationConfig
	require.NoError(t, yaml.Unmarshal(data, &back))
	require.Equal(t, c, back)
}

func TestWriteChainCertsNamesEveryHost(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, WriteChainCerts(dir, []string{"chain", "lab.example"}))
	pemBytes, err := os.ReadFile(filepath.Join(dir, "rpc.cert"))
	require.NoError(t, err)
	block, _ := pem.Decode(pemBytes)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	for _, h := range []string{"chain", "lab.example", "localhost"} {
		require.NoError(t, cert.VerifyHostname(h), h)
	}

	before := pemBytes
	require.NoError(t, WriteChainCerts(dir, []string{"other"}))
	after, err := os.ReadFile(filepath.Join(dir, "rpc.cert"))
	require.NoError(t, err)
	require.Equal(t, before, after, "an existing cert is kept")
}

func TestCheckChainNotReset(t *testing.T) {
	require.NoError(t, CheckChainNotReset(212, 212, 211))
	require.NoError(t, CheckChainNotReset(0, 0, 0), "a fresh stack")
	require.True(t, errors.Is(CheckChainNotReset(0, 222, 0), ErrChainBehindNodes), "a restarted lokid")
}
