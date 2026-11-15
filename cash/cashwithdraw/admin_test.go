package cashwithdraw

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
)

// The operator's payout detail lists every version of the payout's batch, a
// fee bump included, with the output each pays, and the archive facts; a
// payout of another Cash Hub, or an unknown id, is not found.
func TestGetPayout_ListsEveryVersionAndFact(t *testing.T) {
	w := newEngineWorld(t, 5)
	wd := w.commitTo(t, 21_000_000)

	d, err := w.s.GetPayout(w.hub.ID, wd.ID)
	require.NoError(t, err)
	assert.Equal(t, db.CashWithdrawQueued, d.State)
	assert.Empty(t, d.Versions, "no version before the batch window")

	w.passAfterWindow(t)
	first := w.versions(t)[0]
	w.chain.Withhold(*first.Txid, true)
	w.chain.Mine(6)
	w.pass(t)
	require.Len(t, w.versions(t), 2)
	w.chain.Withhold(*first.Txid, false)
	w.chain.Mine(6)
	w.pass(t)

	d, err = w.s.GetPayout(w.hub.ID, wd.ID)
	require.NoError(t, err)
	assert.Equal(t, db.CashWithdrawFinal, d.State)
	require.Len(t, d.Versions, 2)
	for i, v := range d.Versions {
		assert.Equal(t, i, v.Seq-d.Versions[0].Seq)
		assert.True(t, v.Pays)
		require.NotNil(t, v.Vout)
		assert.NotEmpty(t, v.Inputs)
		assert.NotNil(t, v.FeeRateSatKvb)
		assert.NotNil(t, v.FirstBroadcastAt)
	}
	assert.Greater(t, *d.Versions[1].FeeRateSatKvb, *d.Versions[0].FeeRateSatKvb)
	require.NotNil(t, d.Txid)
	assert.Equal(t, *d.Versions[1].Txid, *d.Txid, "the confirmed version")
	var states []string
	for _, f := range d.Facts {
		states = append(states, f.State)
	}
	assert.Contains(t, states, db.CashWithdrawQueued)
	assert.Contains(t, states, db.CashWithdrawFinal)
	assert.NotNil(t, d.FinalAt)

	_, err = w.s.GetPayout(w.hub.ID+1000, wd.ID)
	assert.ErrorIs(t, err, ErrPayoutNotFound)
	_, err = w.s.GetPayout(w.hub.ID, wd.ID+1000)
	assert.ErrorIs(t, err, ErrPayoutNotFound)

	rows, err := w.s.ListPayouts(w.hub.ID, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, d.PayoutRow, rows[0], "the list row is the detail's head")
}
