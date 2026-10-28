package cashwithdraw

import (
	"context"
	"errors"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
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
		if err := e.finalizeAndStore(ctx, p, &pending[i]); err != nil && !errors.Is(err, errSignatureLost) {
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

// signPending signs again, from its stored PSBT, every about-to-sign version
// still without its transaction, as the startup reconcile does: a signature
// that failed (a node timeout, a locked wallet) is retried every pass, not only
// after a restart (R2-P-M1). A failure is logged and retried next pass.
func (e *Engine) signPending(ctx context.Context, p lnclient.OnchainPayer) error {
	var pending []db.CashWithdrawVersion
	if err := e.S.DB.Where("state = ? AND raw_tx IS NULL", db.CashVersionAboutToSign).Order("id").Find(&pending).Error; err != nil {
		return err
	}
	for i := range pending {
		if err := e.finalizeAndStore(ctx, p, &pending[i]); err != nil {
			if errors.Is(err, errSignatureLost) {
				continue
			}
			if errors.Is(err, ErrEngineStopped) {
				return err
			}
			logger.Logger.Warn().Err(err).Uint("version_id", pending[i].ID).Msg("Signing a recorded payout version failed; retried next pass")
		}
	}
	return nil
}
