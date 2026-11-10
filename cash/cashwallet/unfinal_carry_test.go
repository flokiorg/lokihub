package cashwallet

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
)

// A slice made from an ordered slice carries its order payments; a merge
// carries their union (cash_withdraw's unfinal check reads them).
func TestUnfinalPaymentIDsCarried(t *testing.T) {
	w := newCommitWorld(t)
	bill, refs := w.mintBill(t, 5_000)
	ids := "[4]"
	res, err := SplitInTwo(context.TODO(), w.deps, SplitInTwoParams{HubApp: w.hub, SourceWalletApp: bill, Source: refs[0],
		CarvedIdentityType: db.CashIdentityPubkey, CarvedIdentityValue: newPubkey(), CarvedAmountMloki: 2_000,
		RemainderIdentityType: refs[0].IdentityType, RemainderIdentityValue: refs[0].IdentityValue, RemainderAmountMloki: 3_000,
		Ordered: true, UnfinalPaymentIDs: &ids, ExpiresAt: bill.ExpiresAt})
	require.NoError(t, err)
	var made []db.CashWalletClaim
	require.NoError(t, w.svc.DB.Where("wallet_app_id IN ?", []uint{res.Carved.WalletApp.ID, res.Remainder.WalletApp.ID}).Find(&made).Error)
	require.Len(t, made, 2)
	for _, c := range made {
		assert.True(t, c.Ordered)
		require.NotNil(t, c.UnfinalPaymentIDs)
		assert.Equal(t, ids, *c.UnfinalPaymentIDs)
	}

	a, b := w.mintBill(t, 1_000)
	c, d := w.mintBill(t, 2_000)
	merged := "[4,9]"
	out, err := Consolidate(context.TODO(), w.deps, ConsolidateParams{HubApp: w.hub,
		Sources:         []ConsolidateSource{{WalletApp: a, Slice: b[0], AmountMloki: 1_000}, {WalletApp: c, Slice: d[0], AmountMloki: 2_000}},
		NewIdentityType: db.CashIdentityPubkey, NewIdentityValue: newPubkey(), Ordered: true, UnfinalPaymentIDs: &merged})
	require.NoError(t, err)
	var mc db.CashWalletClaim
	require.NoError(t, w.svc.DB.Where("wallet_app_id = ?", out.WalletApp.ID).First(&mc).Error)
	require.NotNil(t, mc.UnfinalPaymentIDs)
	assert.Equal(t, merged, *mc.UnfinalPaymentIDs)
}
