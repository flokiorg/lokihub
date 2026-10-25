package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
