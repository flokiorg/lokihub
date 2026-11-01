package main

import (
	"encoding/hex"
	"testing"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gopkg.in/macaroon-bakery.v2/bakery"
	"gopkg.in/macaroon.v2"
)

func bakeMacaroon(t *testing.T, ops ...*lnrpc.Op) string {
	t.Helper()
	idProto, err := proto.Marshal(&lnrpc.MacaroonId{Nonce: []byte("n"), StorageId: []byte("0"), Ops: ops})
	require.NoError(t, err)
	mac, err := macaroon.New([]byte("root-key-root-key-root-key-12345"),
		append([]byte{byte(bakery.LatestVersion)}, idProto...), "flnd", macaroon.LatestVersion)
	require.NoError(t, err)
	b, err := mac.MarshalBinary()
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func announcingNode(macHex string) *config.NodeConfig {
	return &config.NodeConfig{
		Name: "n",
		Announce: &config.AnnounceConfig{
			Enabled: true,
			Addrs:   []string{"203.0.113.7:9735"},
		},
		Lnd: &config.LndConfig{Macaroon: config.InlineSecret(macHex)},
	}
}

func TestCheckAnnounceRequirements(t *testing.T) {
	signer := bakeMacaroon(t, &lnrpc.Op{Entity: "signer", Actions: []string{"generate"}})
	noSigner := bakeMacaroon(t, &lnrpc.Op{Entity: "info", Actions: []string{"read"}})
	off := &config.NodeConfig{Name: "off"}

	relays, err := checkAnnounceRequirements([]*config.NodeConfig{off}, "")
	assert.NoError(t, err, "no announcing node needs nothing")
	assert.Empty(t, relays)

	_, err = checkAnnounceRequirements([]*config.NodeConfig{off}, "https://not-a-relay")
	assert.Error(t, err, "a malformed env var is refused even with announcing off")

	_, err = checkAnnounceRequirements([]*config.NodeConfig{announcingNode(signer)}, "")
	assert.ErrorContains(t, err, announceRelaysEnv)

	_, err = checkAnnounceRequirements([]*config.NodeConfig{off, announcingNode(noSigner)}, "wss://relay.example.com")
	assert.ErrorContains(t, err, "signer:generate")
	assert.ErrorContains(t, err, "node 1")

	noLnd := announcingNode(signer)
	noLnd.Lnd = nil
	_, err = checkAnnounceRequirements([]*config.NodeConfig{noLnd}, "wss://relay.example.com")
	assert.Error(t, err)

	relays, err = checkAnnounceRequirements([]*config.NodeConfig{announcingNode(signer)}, "wss://a.example, wss://b.example")
	require.NoError(t, err)
	assert.Equal(t, []string{"wss://a.example", "wss://b.example"}, relays)
}
