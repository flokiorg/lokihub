package cashwithdraw

import (
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// The regtest suite's withdraw hooks (plan WP-20 item 0h, G1): the bound and
// starve holds refuse at step 3 with their codes, the slice still claimable.
func TestEngineHooks_BoundAndStarve(t *testing.T) {
	w := newEngineWorld(t, 5)
	bill, hs := w.mintPubkey(t, 21_000_000, 1_000_000)
	cred := nipcash.BySigning(hs[0].priv)

	w.hooks.UnconfirmedBoundMillis.Store(1)
	q := w.mustQuote(t, bill, hs[0], freshAddress(t))
	_, err := w.withdraw(t, bill, hs[0], request(t, bill, cred, q, newRID(), nil))
	requireCode(t, err, constants.ERROR_RATE_LIMITED, "")
	w.hooks.UnconfirmedBoundMillis.Store(0)

	w.hooks.StarveCoins.Store(true)
	q = w.mustQuote(t, bill, hs[0], freshAddress(t))
	_, err = w.withdraw(t, bill, hs[0], request(t, bill, cred, q, newRID(), nil))
	requireCode(t, err, constants.ERROR_INSUFFICIENT_BALANCE, "")
	w.hooks.StarveCoins.Store(false)

	q = w.mustQuote(t, bill, hs[0], freshAddress(t))
	_, err = w.withdraw(t, bill, hs[0], request(t, bill, cred, q, newRID(), nil))
	require.NoError(t, err, "claimable after each refusal")
}

// crash_after_sign stops each pass after signing: the version is never
// published; turned off, the restarted engine's reconcile publishes it.
func TestEngineHooks_CrashAfterSign(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.hooks.CrashAfterSign.Store(true)
	w.passAfterWindow(t)
	assert.True(t, w.payout(t, wd.ID).Signed, "the about-to-sign record is written")
	assert.Nil(t, w.payout(t, wd.ID).Txid)
	w.hooks.CrashAfterSign.Store(false)
	require.NoError(t, w.newEngine().Reconcile(t.Context()))
	assert.NotNil(t, w.payout(t, wd.ID).Txid)
}

// CT-WD-20: with every payout signature lost, a payout with an about-to-sign
// record and nothing broadcast is invalidated from sent_by + 1 day, and
// cancelled once the invalidation is final: the rest of the engine runs on.
func TestEngineHooks_CrashAfterSignInvalidates(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.hooks.CrashAfterSign.Store(true)
	w.passAfterWindow(t)
	require.True(t, w.payout(t, wd.ID).Signed)
	require.Nil(t, w.payout(t, wd.ID).Txid)
	for range 12 {
		w.advance(6 * time.Hour)
		w.chain.Mine(1)
		w.pass(t)
		if w.payout(t, wd.ID).State == db.CashWithdrawCancelled {
			break
		}
	}
	assert.Equal(t, db.CashWithdrawCancelled, w.payout(t, wd.ID).State)
	var inval int
	for _, v := range w.versions(t) {
		if v.Kind == db.CashVersionInvalidation {
			inval++
			assert.Contains(t, []string{db.CashVersionConfirmed, db.CashVersionFinal}, v.State)
		}
	}
	assert.Equal(t, 1, inval)
}

// crash_at stops each pass at the named stage: at AfterStore the version is
// signed and stored but never published until the restart.
func TestEngineHooks_CrashAt(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)
	w.hooks.CrashAt.Store(EngineAfterStore)
	w.passAfterWindow(t)
	vs := w.versions(t)
	require.Len(t, vs, 1)
	assert.Equal(t, db.CashVersionSigned, vs[0].State)
	assert.False(t, w.chain.InMempool(*vs[0].Txid))
	w.hooks.CrashAt.Store("")
	require.NoError(t, w.newEngine().Reconcile(t.Context()))
	assert.True(t, w.chain.InMempool(*vs[0].Txid))
	assert.Equal(t, *vs[0].Txid, *w.payout(t, wd.ID).Txid)
}

// The withdraw-scoped clock moves CancelDue and leaves quotes and calls on the
// ordinary clock.
func TestCancelClockOffset(t *testing.T) {
	w := newEngineWorld(t, 5)
	w.hooks.HoldSign.Store(true)
	wd := w.commitTo(t, 21_000_000)
	w.passAfterWindow(t)
	w.s.CancelClockOffset.Store(wd.SentBy - w.clock().Unix() + 86400)
	t.Cleanup(func() { w.s.CancelClockOffset.Store(0) })
	w.pass(t)
	assert.Equal(t, db.CashWithdrawCancelled, w.payout(t, wd.ID).State)

	// A quote's sent_by is still from the ordinary clock.
	bill, hs := w.mintPubkey(t, 21_000_000)
	q := w.mustQuote(t, bill, hs[0], freshAddress(t))
	assert.Less(t, q.SentBy, w.clock().Add(30*24*time.Hour).Unix())
}
