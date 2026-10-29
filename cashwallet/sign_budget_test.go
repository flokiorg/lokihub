package cashwallet

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// slowSigner is a node whose SignMessage takes delay, or until its context
// ends, and that counts its identity reads.
type slowSigner struct {
	*tests.MockLn
	delay    time.Duration
	getInfos atomic.Int32
}

func (s *slowSigner) SignMessage(ctx context.Context, message string) (string, error) {
	select {
	case <-time.After(s.delay):
		return s.MockLn.SignMessage(ctx, message)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *slowSigner) GetInfo(ctx context.Context) (*lnclient.NodeInfo, error) {
	s.getInfos.Add(1)
	return s.MockLn.GetInfo(ctx)
}

func TestSignBudget_GrowsByAShareForEachBill(t *testing.T) {
	assert.Equal(t, signBudgetBase, SignBudget(0))
	assert.Equal(t, signBudgetBase, SignBudget(-1))
	assert.Equal(t, signBudgetBase+signBudgetPerBill, SignBudget(1))
	assert.Equal(t, signBudgetBase+16*signBudgetPerBill, SignBudget(16))
}

// A node that answers each signature just inside the per-signature timeout
// fails a two-bill split once the commit's one signing budget is spent, rather
// than holding the write lock for a timeout per bill, and the commit leaves no
// bill, slice change or transfer behind.
func TestCommit_StuckSignerFailsWithinTheBudgetAndLeavesNothing(t *testing.T) {
	w := newCommitWorld(t)
	bill, refs := w.mintBill(t, 3000, 2000)
	ln, ok := w.deps.LNClient.(*tests.MockLn)
	require.True(t, ok)
	deps := w.deps
	deps.LNClient = &slowSigner{MockLn: ln, delay: signBudgetBase * 5 / 6}
	deps.Hooks = nil

	var appsBefore int64
	require.NoError(t, w.svc.DB.Model(&db.App{}).Count(&appsBefore).Error)
	start := time.Now()
	_, err := SplitInTwo(context.TODO(), deps, SplitInTwoParams{
		HubApp:                 w.hub,
		SourceWalletApp:        bill,
		Source:                 refs[0],
		CarvedIdentityType:     db.CashIdentityPubkey,
		CarvedIdentityValue:    randomHex(t, 32),
		CarvedAmountMloki:      1000,
		RemainderIdentityType:  refs[0].IdentityType,
		RemainderIdentityValue: refs[0].IdentityValue,
		RemainderAmountMloki:   2000,
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, SignBudget(2)+500*time.Millisecond, "the commit outlived its signing budget")
	var appsAfter int64
	require.NoError(t, w.svc.DB.Model(&db.App{}).Count(&appsAfter).Error)
	assert.Equal(t, appsBefore, appsAfter)
	var source db.CashWalletClaim
	require.NoError(t, w.svc.DB.First(&source, refs[0].ClaimID).Error)
	assert.Nil(t, source.ClaimedAt, "the source slice stays claimable")
}

// A node that signs promptly mints several bills in one commit and reads its
// identity once for the whole commit, not once per bill.
func TestCommit_PromptSignerMintsAndReadsTheIdentityOnce(t *testing.T) {
	w := newCommitWorld(t)
	ln, ok := w.deps.LNClient.(*tests.MockLn)
	require.True(t, ok)
	prompt := &slowSigner{MockLn: ln, delay: signBudgetPerBill / 5}
	deps := w.deps
	deps.LNClient = prompt
	deps.Hooks = nil

	bill, refs := w.mintBill(t, 3000, 2000)
	before := prompt.getInfos.Load()
	_, err := SplitInTwo(context.TODO(), deps, SplitInTwoParams{
		HubApp:                 w.hub,
		SourceWalletApp:        bill,
		Source:                 refs[0],
		CarvedIdentityType:     db.CashIdentityPubkey,
		CarvedIdentityValue:    randomHex(t, 32),
		CarvedAmountMloki:      1000,
		RemainderIdentityType:  refs[0].IdentityType,
		RemainderIdentityValue: refs[0].IdentityValue,
		RemainderAmountMloki:   2000,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), prompt.getInfos.Load()-before, "one identity read per commit")
}
