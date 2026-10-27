package cashwithdraw

import (
	"context"
	"errors"

	"github.com/flokiorg/lokihub/db"
)

// Reconcile is step 11, before anything is offered: every coin set aside or in
// a version re-leased; every about-to-sign version without its transaction
// signed again from its stored PSBT (same inputs and outputs, so any earlier
// signature of it conflicts with this one), checked and stored; the chain read;
// signed versions published (unless an invalidation is due). Nothing is ever
// forgotten: a version that may have been signed counts forever in its batch.
func (e *Engine) Reconcile(ctx context.Context) error {
	e.passMu.Lock()
	defer e.passMu.Unlock()
	p := e.S.payer()
	if p == nil {
		return errors.New("no on-chain capability")
	}
	best, err := p.BestBlock(ctx)
	if err != nil {
		return err
	}
	if err := e.coinStep(ctx, p, best.Height); err != nil {
		return err
	}
	id, err := e.lease()
	if err != nil {
		return err
	}
	if err := e.leaseCoins(ctx, p, id, true); err != nil {
		return err
	}
	var pending []db.CashWithdrawVersion
	if err := e.S.DB.Where("state = ? AND raw_tx IS NULL", db.CashVersionAboutToSign).Order("id").Find(&pending).Error; err != nil {
		return err
	}
	for i := range pending {
		if err := e.finalizeAndStore(ctx, p, &pending[i]); err != nil {
			return err
		}
	}
	if err := e.chainStep(ctx, p, best.Height); err != nil {
		return err
	}
	if err := e.broadcastStep(ctx, p, best.Height); err != nil {
		return err
	}
	e.ready.Store(true)
	return nil
}
