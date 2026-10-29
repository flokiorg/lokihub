package cashwithdraw

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
)

// The operator's own spend may take what the wallet's coin selection would
// spend, less the floor and what payouts still need from those coins: a coin
// another lease holds is not spendable, and a set-aside coin our lease covers
// owes nothing, but one whose lease lapsed does.
func TestSpendCheck_CountsOnlyCoinsTheWalletWouldSpend(t *testing.T) {
	w := newEngineWorld(t, 3)
	foreign := w.chain.Fund(coinSat, 6)
	w.chain.LeaseForeign(foreign)
	w.chain.Reserve = 10_000
	wd := w.commitTo(t, 21_000_000)
	w.hooks.HoldSign.Store(true)
	w.pass(t)
	var aside []db.CashWithdrawCoin
	require.NoError(t, w.svc.DB.Where("withdrawal_id = ? AND state = ?", wd.ID, db.CashCoinSetAside).Find(&aside).Error)
	require.Len(t, aside, 1)
	id, err := w.eng.lease()
	require.NoError(t, err)
	require.Positive(t, w.chain.LeaseOf(id, aside[0].Outpoint))

	spendable, err := w.chain.SpendableBalance(context.TODO())
	require.NoError(t, err)
	assert.Equal(t, int64(2*coinSat), spendable, "neither the foreign-leased coin nor our set-aside one")
	room := uint64(spendable - w.eng.floorSat.Load()) //nolint:gosec // positive
	assert.NoError(t, w.eng.SpendCheck(context.TODO(), room, false), "up to the floor")
	assert.ErrorIs(t, w.eng.SpendCheck(context.TODO(), room+1, false), ErrSpendSetAside, "the foreign-leased coin is not ours to count")

	// A set-aside coin whose lease of ours lapsed is one the wallet would
	// spend: the payout's need comes out of the room.
	lapsed := w.s.now().Unix() - 1
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawCoin{}).Where("outpoint = ?", aside[0].Outpoint).Update("leased_until", lapsed).Error)
	owed, err := w.eng.owedFromUnleasedCoins()
	require.NoError(t, err)
	assert.Equal(t, aside[0].ValueSat, owed)

	// A set-aside short of coins owes its shortfall.
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawCoin{}).Where("outpoint = ?", aside[0].Outpoint).
		Updates(map[string]interface{}{"leased_until": w.s.now().Unix() + 86400, "state": db.CashCoinVanished}).Error)
	var a db.CashWithdrawSetAside
	require.NoError(t, w.svc.DB.Where("withdrawal_id = ?", wd.ID).First(&a).Error)
	owed, err = w.eng.owedFromUnleasedCoins()
	require.NoError(t, err)
	assert.Equal(t, a.AmountSat, owed)
	assert.ErrorIs(t, w.eng.SpendCheck(context.TODO(), room-uint64(a.AmountSat)+1, false), ErrSpendSetAside) //nolint:gosec // positive
	assert.NoError(t, w.eng.SpendCheck(context.TODO(), room-uint64(a.AmountSat), false))                     //nolint:gosec // positive
	assert.ErrorIs(t, w.eng.SpendCheck(context.TODO(), 1, true), ErrSpendSetAside, "no send-all while anything is set aside")
}
