package cashwithdraw

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nip01"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests/fakechain"
)

// spySender records state notices.
type spySender struct {
	mu   sync.Mutex
	sent []sentNotice
}

type sentNotice struct {
	relay string
	ev    *nip01.Event
}

func (s *spySender) SendNotice(_ context.Context, relay string, ev *nip01.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, sentNotice{relay, ev})
	return nil
}

func (s *spySender) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.sent) }

// engineWorld is a world whose payouts are paid by the engine on a fake chain.
type engineWorld struct {
	*world
	chain  *fakechain.Chain
	eng    *Engine
	sender *spySender
	hooks  *EngineHooks
}

const coinSat = 100_000_000

func newEngineWorld(t *testing.T, coins int) *engineWorld {
	t.Helper()
	w := newWorld(t)
	// A one-second batch window keeps the clock near the proofs' time.
	w.svc.Cfg.GetEnv().CashWithdrawBatchWindowSecs = 1
	chain := fakechain.New()
	chain.SetNow(w.clock)
	for range coins {
		chain.Fund(coinSat, 0)
	}
	chain.Mine(10)
	w.node.payer = chain
	w.node.tipFn = chain.Height
	ew := &engineWorld{world: w, chain: chain, sender: &spySender{}, hooks: &EngineHooks{}}
	ew.eng = ew.newEngine()
	require.NoError(t, ew.eng.Reconcile(context.TODO()))
	return ew
}

// newEngine is a fresh engine on the same database and chain: what a restart
// after a crash starts with.
func (w *engineWorld) newEngine() *Engine {
	e := NewEngine(w.s)
	e.Hooks = w.hooks
	e.Notices = w.sender
	w.eng = e
	return e
}

func (w *engineWorld) pass(t *testing.T) {
	t.Helper()
	err := w.eng.Pass(context.TODO())
	if err != nil && !errors.Is(err, ErrEngineStopped) {
		require.NoError(t, err)
	}
}

// passAfterWindow moves the clock past the batch window and runs a pass.
func (w *engineWorld) passAfterWindow(t *testing.T) {
	t.Helper()
	w.advance(time.Duration(w.s.env().BatchWindowSecs()+1) * time.Second)
	w.pass(t)
}

func (w *engineWorld) payout(t *testing.T, id uint) *db.CashWithdrawal {
	t.Helper()
	var wd db.CashWithdrawal
	require.NoError(t, w.svc.DB.First(&wd, id).Error)
	return &wd
}

func (w *engineWorld) versions(t *testing.T) []db.CashWithdrawVersion {
	t.Helper()
	var vs []db.CashWithdrawVersion
	require.NoError(t, w.svc.DB.Order("id").Find(&vs).Error)
	return vs
}

// paidCount is how many confirmed transactions pay the payout's exact output.
func (w *engineWorld) paidCount(t *testing.T, wd *db.CashWithdrawal) int {
	t.Helper()
	script, err := hex.DecodeString(wd.OutputScript)
	require.NoError(t, err)
	return len(w.chain.ConfirmedPaying(script, wd.PayoutMillis/1000))
}

// commitTo commits a payout to a fresh address (the slice's quote) and returns it.
func (w *engineWorld) commitTo(t *testing.T, amount uint64) *db.CashWithdrawal {
	t.Helper()
	bill, hs := w.mintPubkey(t, amount, 1_000_000)
	q := w.mustQuote(t, bill, hs[0], freshAddress(t))
	rid := newRID()
	_, err := w.withdraw(t, bill, hs[0], request(t, bill, nipcash.BySigning(hs[0].priv), q, rid, nil))
	require.NoError(t, err)
	var wd db.CashWithdrawal
	require.NoError(t, w.svc.DB.Where("request_id = ?", rid).First(&wd).Error)
	return &wd
}
