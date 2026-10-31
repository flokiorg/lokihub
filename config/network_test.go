package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckNodeNetwork_RefusesAnUnknownValueAndAnotherChain(t *testing.T) {
	for _, ok := range [][2]string{
		{"", "bitcoin"}, {"flokicoin", "bitcoin"}, {"mainnet", "mainnet"}, {"bitcoin", "bitcoin"},
		{"testnet", "testnet"}, {"testnet", "testnet4"}, {"regtest", "regtest"}, {"signet", "signet"}, {"simnet", "simnet"},
	} {
		assert.NoError(t, CheckNodeNetwork(ok[0], ok[1]), "%v", ok)
	}
	for _, bad := range [][2]string{
		{"regtest", "bitcoin"}, {"", "regtest"}, {"flokicoin", "testnet"}, {"testnet", "bitcoin"}, {"signet", "regtest"},
	} {
		assert.ErrorContains(t, CheckNodeNetwork(bad[0], bad[1]), "but the node is on", "%v", bad)
	}
	err := CheckNodeNetwork("regtst", "regtest")
	assert.ErrorContains(t, err, "is not one of")
	assert.ErrorContains(t, err, "simnet", "the message lists every accepted value")
	assert.Error(t, CheckNodeNetwork("Regtest", "regtest"), "values are exact")
}

func TestValidateNetwork(t *testing.T) {
	for _, ok := range []string{"", "flokicoin", "regtest", "bitcoin", "mainnet", "testnet", "signet"} {
		require.NoError(t, ValidateNetwork(ok), ok)
	}
	for _, bad := range []string{"Regtest", "main", "simnet", "litecoin", " regtest"} {
		require.Error(t, ValidateNetwork(bad), bad)
	}
}

func TestCheckDevStackNode(t *testing.T) {
	require.NoError(t, CheckDevStackNode(false, "bitcoin"), "outside the dev stack any network runs")
	require.NoError(t, CheckDevStackNode(true, "regtest"))
	for _, n := range []string{"bitcoin", "mainnet", "testnet", "signet", ""} {
		require.Error(t, CheckDevStackNode(true, n), n)
	}
}
