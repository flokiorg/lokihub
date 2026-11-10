package cashwallet

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
)

func (w *commitWorld) liveBills(t *testing.T) int {
	t.Helper()
	var n int64
	require.NoError(t, w.svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", w.hub.ID, db.AppKindCashWallet).Count(&n).Error)
	return int(n)
}

func (w *commitWorld) fullSpinOff(t *testing.T, bill *db.App, ref apps.SliceRef, amount uint64, c *LiveBillCap) error {
	t.Helper()
	deps := w.deps
	deps.Hooks = nil
	_, err := SplitInTwo(context.TODO(), deps, SplitInTwoParams{
		HubApp:              w.hub,
		SourceWalletApp:     bill,
		Source:              ref,
		CarvedIdentityType:  db.CashIdentityPubkey,
		CarvedIdentityValue: randomHex(t, 32),
		CarvedAmountMloki:   amount,
		LiveBillCap:         c,
	})
	return err
}

// Two spin-offs on different bills that each passed the early count with one
// bill of room left: the commit counts again under the Hub's lock, so the second
// is refused and leaves nothing.
func TestCommit_LiveBillCapIsCountedInsideTheCommit(t *testing.T) {
	w := newCommitWorld(t)
	billA, refsA := w.mintBill(t, 1000)
	billB, refsB := w.mintBill(t, 1000)
	live := w.liveBills(t)
	capped := &LiveBillCap{Limit: live + 1, Adds: 1}

	require.NoError(t, w.fullSpinOff(t, billA, refsA[0], 1000, capped))
	err := w.fullSpinOff(t, billB, refsB[0], 1000, capped)
	require.ErrorIs(t, err, ErrLiveBillCap)

	assert.Equal(t, live+1, w.liveBills(t))
	var source db.CashWalletClaim
	require.NoError(t, w.svc.DB.First(&source, refsB[0].ClaimID).Error)
	assert.Nil(t, source.ClaimedAt, "the refused source stays claimable")
}

// The cap admits a commit that lands exactly on it, and a commit that adds no
// bill, or has no cap, is never counted.
func TestCommit_LiveBillCapAdmitsUpToTheCap(t *testing.T) {
	w := newCommitWorld(t)
	billA, refsA := w.mintBill(t, 3000)
	live := w.liveBills(t)
	deps := w.deps
	deps.Hooks = nil

	// A partial split adds two bills: refused one below, admitted at the cap.
	split := func(limit int) error {
		_, err := SplitInTwo(context.TODO(), deps, SplitInTwoParams{
			HubApp: w.hub, SourceWalletApp: billA, Source: refsA[0],
			CarvedIdentityType: db.CashIdentityPubkey, CarvedIdentityValue: randomHex(t, 32), CarvedAmountMloki: 1000,
			RemainderIdentityType: refsA[0].IdentityType, RemainderIdentityValue: refsA[0].IdentityValue, RemainderAmountMloki: 2000,
			LiveBillCap: &LiveBillCap{Limit: limit, Adds: 2},
		})
		return err
	}
	require.ErrorIs(t, split(live+1), ErrLiveBillCap)
	require.NoError(t, split(live+2))

	billB, refsB := w.mintBill(t, 1000)
	full := w.liveBills(t)
	assert.NoError(t, w.fullSpinOff(t, billB, refsB[0], 1000, &LiveBillCap{Limit: full, Adds: 0}), "adds no bill net")
	billC, refsC := w.mintBill(t, 1000)
	assert.NoError(t, w.fullSpinOff(t, billC, refsC[0], 1000, &LiveBillCap{Limit: 0, Adds: 1}), "0 is no cap")
	billD, refsD := w.mintBill(t, 1000)
	assert.NoError(t, w.fullSpinOff(t, billD, refsD[0], 1000, nil))
}
