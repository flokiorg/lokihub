//go:build integration

package integration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// stackHooks drives the regtest dev stack's test hooks over the admin API (a
// testhooks build with LOKIHUB_TEST_HOOKS=1): the chain and payer hooks of the
// regtest stack, and the payout engine's holds and withdraw-scoped clock.
type stackHooks struct{ admin *adminClient }

// errEvictUnsupported: the stack cannot drop one transaction from lokid's
// mempool (no RPC removes one, and a chain restart wipes the regtest chain
// until lokid gains --keepregtestdb).
var errEvictUnsupported = errors.New("the regtest stack cannot evict a transaction")

// requireStackHooks returns the hooks, skipping the test when the stack does
// not serve the ones named (paths under /api/test, probed with GET or POST).
func requireStackHooks(t *testing.T, cfg *Config, probes ...string) *stackHooks {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured")
	}
	h := &stackHooks{admin: admin}
	for _, p := range probes {
		method, path, _ := strings.Cut(p, " ")
		var err error
		if method == http.MethodGet {
			err = admin.do(http.MethodGet, "/api/test"+path, nil)
		} else {
			err = admin.doBody(http.MethodPost, "/api/test"+path, map[string]any{}, nil)
		}
		if err != nil && (strings.Contains(err.Error(), "status 404") || strings.Contains(err.Error(), "status 405")) {
			t.Skipf("skipping: the stack serves no %s hook (testhooks build with LOKIHUB_TEST_HOOKS=1)", path)
		}
	}
	return h
}

// Mine mines n blocks to the payer, leaving out withheld transactions.
func (h *stackHooks) Mine(_ context.Context, n int) error {
	return h.admin.doBody(http.MethodPost, "/api/test/chain/mine", map[string]any{"blocks": n}, nil)
}

// Withhold keeps txid out of mined blocks while on.
func (h *stackHooks) Withhold(txid string, on bool) error {
	return h.admin.doBody(http.MethodPost, "/api/test/chain/withhold", map[string]any{"txid": txid, "on": on}, nil)
}

// InvalidateTip reorganises the last block away: it is invalidated and two new
// blocks are mined, its transactions back in the mempool.
func (h *stackHooks) InvalidateTip(_ context.Context) error {
	return h.admin.doBody(http.MethodPost, "/api/test/chain/reorg", map[string]any{"depth": 1}, nil)
}

// Evict is not available on this stack (errEvictUnsupported).
func (h *stackHooks) Evict(context.Context, string) error { return errEvictUnsupported }

// Hooks sets the payout engine's holds: every hold not named is cleared.
func (h *stackHooks) Hooks(_ context.Context, set map[string]bool) error {
	body := map[string]any{"reset": true}
	for k, v := range set {
		body[k] = v
	}
	return h.admin.doBody(http.MethodPost, "/api/test/withdraw/hooks", body, nil)
}

// SetHook sets one payout engine hook to a value (a bool or a number),
// leaving the others.
func (h *stackHooks) SetHook(name string, value any) error {
	return h.admin.doBody(http.MethodPost, "/api/test/withdraw/hooks", map[string]any{name: value}, nil)
}

// ClockOffset moves the backend's withdraw-scoped clock.
func (h *stackHooks) ClockOffset(_ context.Context, secs int64) error {
	return h.admin.doBody(http.MethodPost, "/api/test/clock", map[string]any{"offset_secs": secs, "scope": "withdraw"}, nil)
}

// PayAddress pays amountMillis on-chain from the payer node to address.
func (h *stackHooks) PayAddress(address string, amountMillis uint64) (string, error) {
	var out struct {
		Txid string `json:"txid"`
	}
	err := h.admin.doBody(http.MethodPost, "/api/test/payer/pay-address",
		map[string]any{"address": address, "amount_millis": amountMillis}, &out)
	return out.Txid, err
}

// PayerAddress is a fresh address of the payer node.
func (h *stackHooks) PayerAddress() (string, error) {
	var out struct {
		Address string `json:"address"`
	}
	err := h.admin.do(http.MethodGet, "/api/test/payer/address", &out)
	return out.Address, err
}
