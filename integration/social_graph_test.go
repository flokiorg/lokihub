//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// socialGraphIdentity mirrors cmd/seedrelay's identity struct - kept as a
// separate copy rather than an import since cmd/seedrelay is a standalone
// `go run`-able command, not a library this package should depend on.
type socialGraphIdentity struct {
	Name    string `json:"name"`
	Privkey string `json:"privkey"`
	Pubkey  string `json:"pubkey"`
}

type socialGraph struct {
	Provider socialGraphIdentity   `json:"provider"`
	Members  []socialGraphIdentity `json:"members"`
}

// loadFixedSocialGraph reads testdata/social_graph.json - real, dedicated
// Nostr identities generated once for this suite's own use (see that file's
// own "_comment"), whose provider->members follow set (d = "integration")
// is (re-)published to this instance's own relay by cmd/seedrelay (via `just
// dev seed-relay` / `just dev reset`), not by this suite at test time.
//
// A test that wants a follow_set identity already known-authorized, without
// paying for a fresh keypair + a live publishFollowSet round trip, can build
// its circle_hub with ProviderPubkey: graph.Provider.Pubkey and FollowSetD:
// "integration", and authorize graph.Members[i].Privkey directly. A pubkey
// that's simply not in graph.Members (e.g. newTestPrivkey(t)'s) is a
// ready-made non-member for rejection-path tests.
func loadFixedSocialGraph(t *testing.T) socialGraph {
	t.Helper()

	data, err := os.ReadFile("testdata/social_graph.json")
	require.NoError(t, err, "read testdata/social_graph.json")

	var graph socialGraph
	require.NoError(t, json.Unmarshal(data, &graph), "parse testdata/social_graph.json")
	require.NotEmpty(t, graph.Provider.Privkey, "testdata/social_graph.json: provider missing")
	require.NotEmpty(t, graph.Members, "testdata/social_graph.json: members missing")

	return graph
}
