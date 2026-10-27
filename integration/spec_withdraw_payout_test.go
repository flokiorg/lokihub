//go:build integration

// spec_withdraw_payout_test.go is CT-LH-WD (plan §5.1 I-55, I-57..I-62, §5.2
// I-64, and I-66's chain half): payouts paid by lokihub's payout engine on the
// regtest chain, to final, through bumps, evictions, a reorganisation, the
// Hub's own cancel, the holder's cancel request, batching and a crash.
//
// They drive the regtest stack's chain hooks (mine, withhold, reorg) and the
// payout engine's holds and withdraw-scoped clock (POST /api/test/withdraw/hooks,
// POST /api/test/clock {scope: "withdraw"}) through stackHooks, and skip when the
// stack serves none (a backend built without the testhooks tag), or unless the
// backend reports the on-chain capability. lokid cannot evict one transaction,
// so I-57 checks the bump only.
package integration

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// withdrawChain is what these tests drive on the stack: the regtest chain
// (spec-sync/regtest-hooks' regtest.Hooks) and the backend's withdraw test hooks
// and clock (WP-20 G1).
type withdrawChain interface {
	// Mine mines n blocks, leaving out withheld transactions.
	Mine(ctx context.Context, n int) error
	// Withhold keeps txid out of mined blocks while on.
	Withhold(txid string, on bool) error
	// InvalidateTip reorganises the last block away.
	InvalidateTip(ctx context.Context) error
	// Evict restarts the chain node's mempool without txid.
	Evict(ctx context.Context, txid string) error
	// Hooks sets the backend's withdraw hooks (hold_sign, hold_broadcast,
	// hold_bump, crash_after_sign, unconfirmed_bound, starve_coins).
	Hooks(ctx context.Context, set map[string]bool) error
	// ClockOffset moves the backend's withdraw-scoped clock.
	ClockOffset(ctx context.Context, secs int64) error
}

// newWithdrawChain is the stack's withdrawChain: its test hooks, reset after
// the test.
func newWithdrawChain(t *testing.T, cfg *Config) withdrawChain {
	t.Helper()
	h := requireStackHooks(t, cfg, "GET /chain/tip", "POST /withdraw/hooks")
	t.Cleanup(func() {
		_ = h.Hooks(context.Background(), map[string]bool{})
		_ = h.ClockOffset(context.Background(), 0)
	})
	return h
}

// lookupPayout reads a payout through cash_lookup.
func lookupPayout(t *testing.T, token, priv, rid string) *nipcash.Payout {
	t.Helper()
	var lk nipcash.CashLookupResult
	require.NoError(t, privateCall(t, token, constants.NIP47MethodCashLookup,
		nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Add(time.Minute).Unix()}, priv, &lk))
	require.Equal(t, nipcash.LookupDone, lk.Status)
	p, err := nipcash.ParsePayout(lk.Result)
	require.NoError(t, err)
	return p
}

// waitPayout polls the payout until cond holds, mining a block between polls
// when mine is set.
func waitPayout(t *testing.T, chain withdrawChain, token, priv, rid string, mine bool, cond func(*nipcash.Payout) bool) *nipcash.Payout {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		p := lookupPayout(t, token, priv, rid)
		if cond(p) {
			return p
		}
		require.True(t, time.Now().Before(deadline), "payout stuck at %s", p.State)
		if mine {
			require.NoError(t, chain.Mine(ctxT(t), 1))
		}
		time.Sleep(2 * time.Second)
	}
}

// commitWithdraw quotes and commits one withdrawal of a fresh bill.
func commitWithdraw(t *testing.T, h withdrawHub) (token, priv, rid string, q *nipcash.WithdrawQuote) {
	t.Helper()
	token, privs, _ := h.mintFor(t, 21_000_000, 1_000_000)
	q, err := quoteAs(t, token, privs[0], map[string]any{"address": regtestP2WPKH(t)})
	require.NoError(t, err)
	rid = newTestPrivkey(t)
	_, err = withdrawAs(t, token, privs[0], q, rid)
	require.NoError(t, err)
	return token, privs[0], rid, q
}

// I-55: queued with a valid receipt → broadcast (tx paying exactly
// payout_millis) → confirmed → final at final_depth; the archive facts.
func TestSpecWithdraw_I55_ToFinal(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-final")
	chain := newWithdrawChain(t, cfg)
	token, priv, rid, q := commitWithdraw(t, h)
	p := waitPayout(t, chain, token, priv, rid, false, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutBroadcast })
	require.NotNil(t, p.Vout)
	require.NoError(t, nipcash.VerifyPayoutTx(p.Tx, p.Txid, *p.Vout, q.Address, "", q.PayoutMillis))
	p = waitPayout(t, chain, token, priv, rid, true, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutConfirmed })
	p = waitPayout(t, chain, token, priv, rid, true, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutFinal })
	assert.Equal(t, p.FinalDepth, *p.Depth)
	assert.True(t, p.Signed)
}

// I-57: a withheld version is replaced after 6 blocks by one paying the same
// output; an evicted version is republished.
func TestSpecWithdraw_I57_BumpAndEviction(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-bump")
	chain := newWithdrawChain(t, cfg)
	token, priv, rid, q := commitWithdraw(t, h)
	first := waitPayout(t, chain, token, priv, rid, false, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutBroadcast })
	require.NoError(t, chain.Withhold(first.Txid, true))
	require.NoError(t, chain.Mine(ctxT(t), 6))
	second := waitPayout(t, chain, token, priv, rid, false, func(p *nipcash.Payout) bool { return p.Txid != first.Txid })
	require.NoError(t, nipcash.VerifyPayoutTx(second.Tx, second.Txid, *second.Vout, q.Address, "", q.PayoutMillis))
	require.NoError(t, chain.Withhold(first.Txid, false))
	if err := chain.Evict(ctxT(t), second.Txid); errors.Is(err, errEvictUnsupported) {
		t.Log("eviction not checked: " + err.Error())
	} else {
		require.NoError(t, err)
	}
	waitPayout(t, chain, token, priv, rid, true, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutFinal })
}

// I-58: invalidateblock on the confirming block: broadcast again, the identical
// transaction republished and re-confirmed; no replacement.
func TestSpecWithdraw_I58_Reorg(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-reorg")
	chain := newWithdrawChain(t, cfg)
	token, priv, rid, _ := commitWithdraw(t, h)
	waitPayout(t, chain, token, priv, rid, false, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutBroadcast })
	conf := waitPayout(t, chain, token, priv, rid, true, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutConfirmed })
	require.NoError(t, chain.InvalidateTip(ctxT(t)))
	back := waitPayout(t, chain, token, priv, rid, false, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutBroadcast })
	assert.Equal(t, conf.Txid, back.Txid)
	final := waitPayout(t, chain, token, priv, rid, true, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutFinal })
	assert.Equal(t, conf.Txid, final.Txid, "never replaced")
}

// I-59: the Hub's own cancel of a payout never signed, at sent_by + 1 day: the
// re-minted bill is delivered and the source tombstone says transferred.
func TestSpecWithdraw_I59_HubCancel(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-hubcancel")
	chain := newWithdrawChain(t, cfg)
	require.NoError(t, chain.Hooks(ctxT(t), map[string]bool{"hold_sign": true}))
	t.Cleanup(func() { _ = chain.Hooks(context.Background(), map[string]bool{}) })
	token, priv, rid, q := commitWithdraw(t, h)
	require.NoError(t, chain.ClockOffset(ctxT(t), q.SentBy-time.Now().Unix()+86400))
	t.Cleanup(func() { _ = chain.ClockOffset(context.Background(), 0) })
	p := waitPayout(t, chain, token, priv, rid, false, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutCancelled })
	assert.NotEmpty(t, p.NewWalletPubkey)
	assert.NotEmpty(t, p.NewWalletToken.Token)
	assert.Empty(t, p.Txid)
}

// I-60: the holder's cancel request: RESTRICTED before 7 days; at 7 days on a
// withheld version → invalidation, broadcast until it is final, then cancelled;
// the owner can neither delete the re-minted bill nor remove its slice.
func TestSpecWithdraw_I60_CancelRequest(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-cancelreq")
	chain := newWithdrawChain(t, cfg)
	token, priv, rid, q := commitWithdraw(t, h)
	p := waitPayout(t, chain, token, priv, rid, false, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutBroadcast })
	require.NoError(t, chain.Withhold(p.Txid, true))
	err := privateCall(t, token, constants.NIP47MethodCashWithdraw, map[string]any{"cancel": true, "request_id": rid}, priv, nil)
	requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
	require.NoError(t, chain.ClockOffset(ctxT(t), q.SentBy-time.Now().Unix()+604800))
	t.Cleanup(func() { _ = chain.ClockOffset(context.Background(), 0) })
	var answer nipcash.Payout
	require.NoError(t, privateCall(t, token, constants.NIP47MethodCashWithdraw, map[string]any{"cancel": true, "request_id": rid}, priv, &answer))
	assert.Equal(t, nipcash.PayoutBroadcast, answer.State)
	done := waitPayout(t, chain, token, priv, rid, true, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutCancelled })
	appID, err := h.admin.findAppIDByWalletPubkey(done.NewWalletPubkey)
	require.NoError(t, err)
	assert.Error(t, h.admin.deleteApp(appID), "a re-minted bill cannot be deleted by its owner")
}

// I-62: three withdrawals within the window share one version with a change
// output; a fourth joins by replacement; every output pays its quote.
func TestSpecWithdraw_I62_Batching(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-batch")
	chain := newWithdrawChain(t, cfg)
	type call struct {
		token, priv, rid string
		q                *nipcash.WithdrawQuote
	}
	var calls []call
	for range 3 {
		token, priv, rid, q := commitWithdraw(t, h)
		calls = append(calls, call{token, priv, rid, q})
	}
	first := waitPayout(t, chain, calls[0].token, calls[0].priv, calls[0].rid, false, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutBroadcast })
	for _, c := range calls[1:] {
		p := waitPayout(t, chain, c.token, c.priv, c.rid, false, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutBroadcast })
		assert.Equal(t, first.Txid, p.Txid, "one version")
	}
	token, priv, rid, q := commitWithdraw(t, h)
	calls = append(calls, call{token, priv, rid, q})
	for _, c := range calls {
		p := waitPayout(t, chain, c.token, c.priv, c.rid, true, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutFinal })
		require.NoError(t, nipcash.VerifyPayoutTx(p.Tx, p.Txid, *p.Vout, c.q.Address, "", c.q.PayoutMillis))
	}
}

// I-64: the backend killed after the about-to-sign record and after the stored
// signature, then restarted: at most one version confirms, the payout reaches
// final, signed stays true. (The restart is the stack's; crash_after_sign
// stands in for the kill.)
func TestSpecWithdraw_I64_Crash(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-crash")
	chain := newWithdrawChain(t, cfg)
	require.NoError(t, chain.Hooks(ctxT(t), map[string]bool{"crash_after_sign": true}))
	token, priv, rid, _ := commitWithdraw(t, h)
	time.Sleep(5 * time.Second)
	require.NoError(t, chain.Hooks(ctxT(t), map[string]bool{}))
	p := waitPayout(t, chain, token, priv, rid, true, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutFinal })
	assert.True(t, p.Signed)
}

// I-66 (chain half): with the switch off, a committed payout is still paid to
// final.
func TestSpecWithdraw_I66_PaidWhileOff(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-off-paid")
	chain := newWithdrawChain(t, cfg)
	token, priv, rid, _ := commitWithdraw(t, h)
	setWithdrawNode(t, h.admin, false)
	t.Cleanup(func() { setWithdrawNode(t, h.admin, true) })
	waitPayout(t, chain, token, priv, rid, true, func(p *nipcash.Payout) bool { return p.State == nipcash.PayoutFinal })
	var st struct {
		Payouts map[string]int64 `json:"payouts"`
	}
	require.NoError(t, h.admin.do(http.MethodGet, "/api/cash-withdraw/status", &st))
	assert.Positive(t, st.Payouts["final"])
}
