package nodesign

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NIP-CASH §Network and Chain (CASH-391): network is one of mainnet, testnet,
// signet or regtest; a network with none of those names maps to "".
func TestNetwork_IsANIP47ValueOrNothing(t *testing.T) {
	for node, want := range map[string]string{
		"bitcoin": "mainnet", "mainnet": "mainnet", "flokicoin": "mainnet", "main": "mainnet",
		"testnet": "testnet", "testnet3": "testnet", "testnet4": "testnet",
		"signet": "signet", "regtest": "regtest", "simnet": "regtest",
		"": "", "Mainnet": "", "devnet": "",
	} {
		assert.Equal(t, want, Network(node), "Network(%q)", node)
	}
}

const idA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const idB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// NIP-CASH §Mint Provenance (CASH-374, CASH-385): a node identity signs cash on
// one network only; the first start records it.
func TestCheckIdentityNetwork_OneNetworkPerIdentity(t *testing.T) {
	first, err := CheckIdentityNetwork("", idA, "regtest")
	require.NoError(t, err)
	assert.JSONEq(t, `{"`+idA+`":"regtest"}`, first, "the first start records the pair")

	again, err := CheckIdentityNetwork(first, idA, "simnet")
	require.NoError(t, err, "the same NIP-47 network starts")
	assert.Empty(t, again, "nothing to record")

	_, err = CheckIdentityNetwork(first, idA, "signet")
	assert.ErrorIs(t, err, ErrIdentityOnAnotherNetwork, "recorded under regtest, the node now on signet")

	other, err := CheckIdentityNetwork(first, idB, "signet")
	require.NoError(t, err, "another identity on another network")
	assert.JSONEq(t, `{"`+idA+`":"regtest","`+idB+`":"signet"}`, other)

	_, err = CheckIdentityNetwork("", idA, "devnet")
	assert.ErrorIs(t, err, ErrUnknownNetwork)
	_, err = CheckIdentityNetwork("{", idA, "regtest")
	assert.Error(t, err, "an unreadable record refuses rather than overwrites")
}

func TestIdentityNetworkGuard_PassesOnceTheCheckPasses(t *testing.T) {
	var g IdentityNetworkGuard
	assert.NoError(t, g.Allow(context.Background()), "no check registered")

	calls, fail := 0, true
	g.Set(func(context.Context) error {
		calls++
		if fail {
			return errors.New("not yet")
		}
		return nil
	})
	assert.Error(t, g.Allow(context.Background()))
	assert.Error(t, g.Allow(context.Background()), "a failed check is tried again")
	fail = false
	assert.NoError(t, g.Allow(context.Background()))
	assert.NoError(t, g.Allow(context.Background()))
	assert.Equal(t, 3, calls, "a passed check is remembered")
}

func TestSigner_RefusesWhileTheGuardRefuses(t *testing.T) {
	t.Cleanup(func() { Guard.Set(nil) })
	Guard.Set(func(context.Context) error { return ErrIdentityOnAnotherNetwork })
	_, err := Signer(context.Background(), nil)("payload")
	assert.ErrorIs(t, err, ErrIdentityOnAnotherNetwork, "refused before the node is asked")
}
