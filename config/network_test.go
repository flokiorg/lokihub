package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
