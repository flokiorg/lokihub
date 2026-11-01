package itest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDevnetSmoke is a minimal connectivity check for the Devnet wrapper
// itself: all three roles respond, have distinct pubkeys, and are funded.
// It's the foundation the scenario runner (T4-1) and seed scenarios (T4-2)
// build on.
func TestDevnetSmoke(t *testing.T) {
	d := StartDevnet(t)

	pubkeys := map[string]string{}
	for _, role := range []string{"lsp", "client", "payer"} {
		client, err := d.LightningClient(role)
		require.NoError(t, err, "role %s", role)

		info, err := client.GetInfo()
		require.NoError(t, err, "GetInfo for role %s", role)
		require.NotEmpty(t, info.Pubkey, "role %s pubkey", role)
		pubkeys[role] = info.Pubkey

		balance, err := client.WalletBalance(t.Context())
		require.NoError(t, err, "WalletBalance for role %s", role)
		assert.Positive(t, balance.ConfirmedBalance, "role %s should be pre-funded by ./collect-addresses.sh + ./generate.sh", role)
	}

	assert.NotEqual(t, pubkeys["lsp"], pubkeys["client"])
	assert.NotEqual(t, pubkeys["lsp"], pubkeys["payer"])
	assert.NotEqual(t, pubkeys["client"], pubkeys["payer"])
}
