package cashwithdraw

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
)

// conserved is Σ app balances + Σ held_millis + Σ amount_millis of final
// payouts: constant through withdrawals, finals and re-mints (U-4.8 extended).
func (w *engineWorld) conserved(t *testing.T) int64 {
	t.Helper()
	var ids []uint
	require.NoError(t, w.svc.DB.Model(&db.App{}).Pluck("id", &ids).Error)
	bals, err := queries.GetIsolatedBalancesByAppIDs(w.svc.DB, ids)
	require.NoError(t, err)
	var sum int64
	for _, b := range bals {
		sum += b
	}
	var held, final int64
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawal{}).Select("COALESCE(SUM(held_millis), 0)").Scan(&held).Error)
	require.NoError(t, w.svc.DB.Model(&db.CashWithdrawal{}).Where("state = ?", db.CashWithdrawFinal).
		Select("COALESCE(SUM(amount_millis), 0)").Scan(&final).Error)
	return sum + held + final
}

// checkVersions: within each batch every version shares an input with every
// earlier version that may have been signed and can still confirm.
func (w *engineWorld) checkVersions(t *testing.T, seed int64) {
	t.Helper()
	vs := w.versions(t)
	byBatch := map[uint][]db.CashWithdrawVersion{}
	for _, v := range vs {
		if v.Kind != db.CashVersionChild {
			byBatch[v.BatchID] = append(byBatch[v.BatchID], v)
		}
	}
	for _, bvs := range byBatch {
		for i := 1; i < len(bvs); i++ {
			var earlier [][]string
			for j := 0; j < i; j++ {
				if bvs[j].State != db.CashVersionInvalid {
					earlier = append(earlier, inputsOf(&bvs[j]))
				}
			}
			require.True(t, nipcash.SpendsFromEach(earlier, inputsOf(&bvs[i])), "seed %d: version %d spends from every earlier one", seed, bvs[i].ID)
		}
	}
}

func propertyRuns() int {
	if s := os.Getenv("WITHDRAW_PROPERTY_RUNS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	// 500 runs take minutes under -race; the acceptance run sets
	// WITHDRAW_PROPERTY_RUNS=500.
	if testing.Short() {
		return 20
	}
	return 100
}

// U-24.3 / U-24.17: over random runs of adds, passes, blocks, reorganisations,
// evictions, withheld and fee-refused versions, cancel requests and a crash at
// every engine stage, no two transactions paying one payout ever confirm,
// every new version spends from each earlier one, value is conserved, and in
// the end every payout is paid exactly payout_millis or re-minted, never both.
func TestU_24_3_Property(t *testing.T) {
	runs := propertyRuns()
	for run := range runs {
		seed := int64(run)*7919 + 17
		t.Run(fmt.Sprint(seed), func(t *testing.T) { propertyRun(t, seed) })
	}
}

func propertyRun(t *testing.T, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	w := newEngineWorld(t, 8)
	n := 1 + rng.Intn(4)
	var ws []*db.CashWithdrawal
	for range n {
		ws = append(ws, w.commitTo(t, uint64(5_000_000+rng.Intn(50)*1_000_000)))
	}
	base := w.conserved(t)
	check := func() {
		for _, wd := range ws {
			require.LessOrEqual(t, w.paidCount(t, w.payout(t, wd.ID)), 1, "seed %d: payout %d paid twice", seed, wd.ID)
		}
		w.checkVersions(t, seed)
		require.Equal(t, base, w.conserved(t), "seed %d: value conserved", seed)
	}
	cancelRequests := func() {
		for _, wd := range ws {
			cur := w.payout(t, wd.ID)
			if cur.State == db.CashWithdrawBroadcast && w.clock().Unix() >= cur.SentBy+604800 {
				_, err := w.s.CancelRequest(cur)
				require.NoError(t, err)
			}
		}
	}
	for range 25 {
		switch rng.Intn(10) {
		case 0, 1:
			w.passAfterWindow(t)
		case 2:
			w.chain.Mine(1 + rng.Intn(3))
			w.pass(t)
		case 3:
			if w.chain.Height() > 115 {
				var drop []string
				if rng.Intn(2) == 0 {
					drop = w.chain.MempoolTxids()
				}
				w.chain.Reorg(1, drop...)
				w.pass(t)
			}
		case 4:
			if ids := w.chain.MempoolTxids(); len(ids) > 0 {
				w.chain.Evict(ids[rng.Intn(len(ids))])
			}
		case 5:
			if ids := w.chain.MempoolTxids(); len(ids) > 0 {
				w.chain.Withhold(ids[rng.Intn(len(ids))], rng.Intn(3) > 0)
			}
		case 6:
			w.advance(time.Duration(rng.Intn(3)) * 86400 * time.Second)
			cancelRequests()
			w.pass(t)
		case 7:
			stage := EngineStages[rng.Intn(len(EngineStages))]
			fired := false
			w.hooks.Before = func(s string) error {
				if s == stage && !fired {
					fired = true
					return ErrEngineStopped
				}
				return nil
			}
			w.pass(t)
			w.hooks.Before = nil
			e := w.newEngine()
			if err := e.Reconcile(context.TODO()); err != nil && !errors.Is(err, ErrEngineStopped) {
				require.NoError(t, err)
			}
		case 8:
			if ids := w.chain.MempoolTxids(); len(ids) > 0 {
				refused := ids[rng.Intn(len(ids))]
				once := true
				w.chain.PublishHook = func(id string) error {
					if id == refused && once {
						once = false
						return lnclient.ErrPublishFeeTooLow
					}
					return nil
				}
				w.chain.Evict(refused)
				w.advance(time.Duration(w.s.env().RebroadcastSecs()) * time.Second)
				w.pass(t)
				w.chain.PublishHook = nil
			}
		case 9:
			w.advance(time.Duration(w.s.env().RebroadcastSecs()) * time.Second)
			w.pass(t)
		}
		check()
	}

	// Settle: the network relays everything again; cancel requests come in.
	w.chain.ReleaseAll()
	w.chain.PublishHook = nil
	w.hooks.Before = nil
	for range 60 {
		done := true
		for _, wd := range ws {
			s := w.payout(t, wd.ID).State
			if s != db.CashWithdrawFinal && s != db.CashWithdrawCancelled {
				done = false
			}
		}
		if done {
			break
		}
		w.advance(86400 * time.Second)
		cancelRequests()
		w.chain.Mine(2)
		w.pass(t)
		check()
	}
	for _, wd := range ws {
		got := w.payout(t, wd.ID)
		switch got.State {
		case db.CashWithdrawFinal:
			require.Equal(t, 1, w.paidCount(t, got), "seed %d: a final payout is paid once", seed)
			require.Nil(t, got.RemintAppID, "seed %d: never both", seed)
		case db.CashWithdrawCancelled:
			require.Equal(t, 0, w.paidCount(t, got), "seed %d: a cancelled payout is never paid", seed)
			require.NotNil(t, got.RemintAppID)
			require.Equal(t, got.AmountMillis, balance(w.world, *got.RemintAppID), "seed %d: re-minted whole", seed)
		default:
			t.Fatalf("seed %d: payout %d ended %s", seed, got.ID, got.State)
		}
	}
}
