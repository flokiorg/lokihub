//go:build integration

// spec_withdraw_hooks_test.go: the stack halves of I-56 (step-3 refusals
// through the bound and starve hooks, and a quote expired by mining) and I-61
// (the Cash Hub teardown waits for an open payout).
package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// I-56 (stack half): RATE_LIMITED at the unconfirmed bound, INSUFFICIENT_BALANCE
// with the coins starved, quote_expired after mining past holds_until; the
// slice is claimable after each.
func TestSpecWithdraw_I56_StepThreeHooks(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-step3")
	hooks := requireStackHooks(t, cfg, "GET /chain/tip", "POST /withdraw/hooks")
	t.Cleanup(func() { _ = hooks.Hooks(context.Background(), map[string]bool{}) })
	token, privs, _ := h.mintFor(t, 21_000_000, 1_000_000)
	quote := func() *nipcash.WithdrawQuote {
		q, err := quoteAs(t, token, privs[0], map[string]any{"address": regtestP2WPKH(t)})
		require.NoError(t, err)
		return q
	}

	require.NoError(t, hooks.SetHook("unconfirmed_bound_millis", 1))
	_, err := withdrawAs(t, token, privs[0], quote(), newTestPrivkey(t))
	requireNWCErrorCode(t, err, constants.ERROR_RATE_LIMITED)
	require.NoError(t, hooks.SetHook("unconfirmed_bound_millis", 0))

	require.NoError(t, hooks.SetHook("starve_coins", true))
	_, err = withdrawAs(t, token, privs[0], quote(), newTestPrivkey(t))
	requireNWCErrorCode(t, err, constants.ERROR_INSUFFICIENT_BALANCE)
	require.NoError(t, hooks.SetHook("starve_coins", false))

	q := quote()
	require.NoError(t, hooks.Mine(ctxT(t), 3))
	_, err = withdrawAs(t, token, privs[0], q, newTestPrivkey(t))
	requireDetail(t, err, constants.ERROR_RESTRICTED, nipcash.DetailQuoteExpired)

	_, err = withdrawAs(t, token, privs[0], quote(), newTestPrivkey(t))
	require.NoError(t, err, "the slice stayed claimable")
}

// I-61: the delete check lists payout_open while a payout is open, and the
// Cash Hub delete is refused.
func TestSpecWithdraw_I61_Teardown(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-teardown")
	hooks := requireStackHooks(t, cfg, "POST /withdraw/hooks")
	// Held unsigned, the payout stays open whatever else runs.
	require.NoError(t, hooks.Hooks(ctxT(t), map[string]bool{"hold_sign": true}))
	t.Cleanup(func() { _ = hooks.Hooks(context.Background(), map[string]bool{}) })
	token, privs, _ := h.mintFor(t, 21_000_000)
	q, err := quoteAs(t, token, privs[0], map[string]any{"address": regtestP2WPKH(t)})
	require.NoError(t, err)
	_, err = withdrawAs(t, token, privs[0], q, newTestPrivkey(t))
	require.NoError(t, err)

	var check struct {
		Blockers []struct {
			Kind  string `json:"kind"`
			Count int64  `json:"count"`
			Until *int64 `json:"until"`
		} `json:"blockers"`
		EarliestDeleteAt *int64 `json:"earliest_delete_at"`
	}
	require.NoError(t, h.admin.do(http.MethodGet, fmt.Sprintf("/api/cash-hubs/%d/delete-check", h.id), &check))
	kinds := map[string]int64{}
	for _, b := range check.Blockers {
		kinds[b.Kind] = b.Count
		if b.Kind == "payout_open" {
			assert.Nil(t, b.Until, "an open payout has no end time")
		}
	}
	assert.EqualValues(t, 1, kinds["payout_open"])
	assert.Nil(t, check.EarliestDeleteAt)
	assert.Error(t, h.admin.deleteApp(h.id), "the Cash Hub stays while its payout is open")
}
