package cashwithdraw

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
)

// The Cash Hub delete check reads the withdrawals (Hub teardown): an owed payout
// blocks with no time, an ended one until its record's retention ends.
func TestHubDeleteCheckReadsWithdrawals(t *testing.T) {
	w := newWorld(t)
	apps.RegisterCashHubDeleteBlocker("cash_withdraw", w.s.HubDeleteBlockers)
	_, _, wd := w.committed(t, false)
	find := func(kind string) *apps.CashHubDeleteBlocker {
		check, err := apps.CashHubDeleteCheckTx(w.svc.DB, w.hub.ID, w.clock())
		require.NoError(t, err)
		for _, b := range check.Blockers {
			if b.Kind == kind {
				return &b
			}
		}
		return nil
	}
	b := find(apps.CashHubBlockerPayoutOpen)
	require.NotNil(t, b, "an owed payout keeps the Hub")
	assert.EqualValues(t, 1, b.Count)
	assert.Nil(t, b.Until)

	end := w.clock().Unix()
	w.setPayout(t, wd, map[string]any{"state": db.CashWithdrawFinal, "final_at": end, "final_reached": true, "held_millis": 0})
	assert.Nil(t, find(apps.CashHubBlockerPayoutOpen))
	b = find(apps.CashHubBlockerWithdrawRecords)
	require.NotNil(t, b)
	require.NotNil(t, b.Until, "a final payout keeps the Hub until its record goes")
	assert.Greater(t, *b.Until, end)
	w.mu.Lock()
	w.now = time.Unix(*b.Until+1, 0)
	w.mu.Unlock()
	assert.Nil(t, find(apps.CashHubBlockerWithdrawRecords))
}
