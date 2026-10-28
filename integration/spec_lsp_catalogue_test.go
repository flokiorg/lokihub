//go:build integration

// spec_lsp_catalogue_test.go is CT-LSP-01 (plan WP-26, I-80..I-84): the LSP
// catalogue built from flspd's NIP-LSP announcement on the dev relay.
//
// It needs flspd on the regtest stack (the compose profile "lsp", on when the
// flspd:regtest image exists; bootstrap funds flnd-lsp and opens its channel to
// the payer) and the dev relay among the Hub's General relays. Every
// test skips when no LSP announcement is in the catalogue. I-83 and I-84 drive
// flspd's dashboard (fee hot-swap, unlist) through the stack's test hooks
// (POST /api/test/flspd/..., or FLSPD_ADMIN_URL when set) and skip without
// them.
package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/niplsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type adminLSP struct {
	NodeKey   string `json:"nodeKey"`
	Pubkey    string `json:"pubkey"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Usable    bool   `json:"usable"`
	Trusted   bool   `json:"trusted"`
	InGraph   bool   `json:"inGraph"`
	Network   string `json:"network"`
	Chain     string `json:"chain"`
	Protocols []int  `json:"protocols"`
	LastCheck *struct {
		Error  string `json:"error"`
		Usable bool   `json:"usable"`
		Worse  bool   `json:"worse"`
		NodeID string `json:"nodeId"`
	} `json:"lastCheck"`
}

// flspdLSP finds flspd's announcement on the dev relay and returns its
// catalogue entry, adding it by node key when the periodic refresh has not
// read it yet.
func flspdLSP(t *testing.T, cfg *Config, admin *adminClient) adminLSP {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relay, err := nostr.RelayConnect(ctx, cfg.RelayURL)
	require.NoError(t, err)
	defer relay.Close()
	evs, err := relay.QuerySync(ctx, nostr.Filter{Kinds: []int{niplsp.Kind}, Limit: 10})
	require.NoError(t, err)
	if len(evs) == 0 {
		t.Skip("no kind-10735 announcement on the dev relay: the stack runs without flspd (build flspd:regtest)")
	}
	key := evs[0].PubKey
	var l adminLSP
	require.NoError(t, admin.doBody(http.MethodPost, "/api/lsps", map[string]any{"pubkey": key}, &l), "I-80: the announcement is found")
	return l
}

func requireAdmin(t *testing.T, cfg *Config) *adminClient {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured")
	}
	return admin
}

// I-80: flspd's announcement is discovered from the dev relay, parsed for
// regtest, and listed untrusted.
func TestSpecLSP_I80_Discovered(t *testing.T) {
	cfg := requireConfig(t)
	admin := requireAdmin(t, cfg)
	l := flspdLSP(t, cfg, admin)
	assert.True(t, l.Usable)
	assert.Equal(t, "flokicoin", l.Chain)
	assert.Equal(t, "regtest", l.Network)
	assert.Equal(t, "open", l.Status)
	assert.ElementsMatch(t, []int{1, 2, 5}, l.Protocols)

	var all []adminLSP
	require.NoError(t, admin.do(http.MethodGet, "/api/lsps", &all))
	found := false
	for _, x := range all {
		found = found || x.NodeKey == l.NodeKey
	}
	assert.True(t, found)
}

// I-81: trusting runs the live check against flspd, which matches its
// announcement.
func TestSpecLSP_I81_TrustLiveCheck(t *testing.T) {
	cfg := requireConfig(t)
	admin := requireAdmin(t, cfg)
	l := flspdLSP(t, cfg, admin)
	require.NoError(t, admin.doBody(http.MethodPut, fmt.Sprintf("/api/lsps/%s/trust", l.NodeKey), map[string]any{"trusted": true}, &l))
	t.Cleanup(func() {
		_ = admin.doBody(http.MethodPut, fmt.Sprintf("/api/lsps/%s/trust", l.NodeKey), map[string]any{"trusted": false}, nil)
	})
	assert.True(t, l.Trusted)
	require.NotNil(t, l.LastCheck)
	assert.Empty(t, l.LastCheck.Error)
	assert.True(t, l.LastCheck.Usable)
	assert.False(t, l.LastCheck.Worse)
	assert.Len(t, l.NodeKey, 64, "the node key is x-only")
	assert.Len(t, l.Pubkey, 66, "the node id resolved")
}

// I-82: a JIT receive through the trusted LSP: LSPS2 get_info then buy.
func TestSpecLSP_I82_JITThroughTrustedLSP(t *testing.T) {
	cfg := requireConfig(t)
	admin := requireAdmin(t, cfg)
	l := flspdLSP(t, cfg, admin)
	require.NoError(t, admin.doBody(http.MethodPut, fmt.Sprintf("/api/lsps/%s/trust", l.NodeKey), map[string]any{"trusted": true}, &l))
	t.Cleanup(func() {
		_ = admin.doBody(http.MethodPut, fmt.Sprintf("/api/lsps/%s/trust", l.NodeKey), map[string]any{"trusted": false}, nil)
	})
	// GET /api/lsps2/info answers the LSP's first opening fee parameter set,
	// which the buy takes back as is.
	var params map[string]any
	require.NoError(t, admin.do(http.MethodGet, "/api/lsps2/info?lsp="+l.Pubkey, &params))
	require.NotEmpty(t, params)
	size := uint64(50_000_000)
	var buy struct {
		InterceptSCID string `json:"interceptScid"`
	}
	require.NoError(t, admin.doBody(http.MethodPost, "/api/lsps2/buy", map[string]any{
		"lspPubkey": l.Pubkey, "paymentSizeMloki": size, "openingFeeParams": params,
	}, &buy))
	assert.NotEmpty(t, buy.InterceptSCID)
}

// flspdAdmin drives flspd's dashboard: POST path (fee/raise, fee/restore,
// announce/unlist, announce/resume) to FLSPD_ADMIN_URL when it is set, else to
// the stack's test hook /api/test/flspd. False when neither answers.
func flspdAdmin(t *testing.T, admin *adminClient, path string) bool {
	t.Helper()
	if hook := os.Getenv("FLSPD_ADMIN_URL"); hook != "" {
		resp, err := http.Post(strings.TrimRight(hook, "/")+"/"+path, "application/json", nil)
		if err != nil {
			t.Logf("FLSPD_ADMIN_URL %s: %v", path, err)
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode < 300
	}
	if err := admin.doBody(http.MethodPost, "/api/test/flspd/"+path, map[string]any{}, nil); err != nil {
		t.Logf("flspd test hook %s: %v", path, err)
		return false
	}
	return true
}

// I-83: a fee hot-swap on flspd makes the live terms worse: the next live
// check says so (the UI asks again before buying).
func TestSpecLSP_I83_WorseLiveTerms(t *testing.T) {
	cfg := requireConfig(t)
	admin := requireAdmin(t, cfg)
	l := flspdLSP(t, cfg, admin)
	require.NoError(t, admin.doBody(http.MethodPut, fmt.Sprintf("/api/lsps/%s/trust", l.NodeKey), map[string]any{"trusted": true}, &l))
	if !flspdAdmin(t, admin, "fee/raise") {
		t.Skip("no hook to hot-swap flspd's fees (POST /api/test/flspd/fee/raise, or FLSPD_ADMIN_URL)")
	}
	t.Cleanup(func() { flspdAdmin(t, admin, "fee/restore") })
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/lsps/%s/check", l.NodeKey), nil, &l))
	require.NotNil(t, l.LastCheck)
	assert.True(t, l.LastCheck.Worse)
}

// I-84: flspd unlists: the closed announcement hides it from new purchases,
// and existing channels stay.
func TestSpecLSP_I84_ClosedHides(t *testing.T) {
	cfg := requireConfig(t)
	admin := requireAdmin(t, cfg)
	l := flspdLSP(t, cfg, admin)
	if !flspdAdmin(t, admin, "announce/unlist") {
		t.Skip("no hook to unlist flspd (POST /api/test/flspd/announce/unlist, or FLSPD_ADMIN_URL)")
	}
	t.Cleanup(func() { flspdAdmin(t, admin, "announce/resume") })
	require.Eventually(t, func() bool {
		_ = admin.doBody(http.MethodPost, "/api/lsps", map[string]any{"pubkey": l.NodeKey}, nil)
		var all []adminLSP
		if admin.do(http.MethodGet, "/api/lsps", &all) != nil {
			return false
		}
		for _, x := range all {
			if x.NodeKey == l.NodeKey {
				return x.Status == "closed"
			}
		}
		return false
	}, 60*time.Second, 2*time.Second)
}
