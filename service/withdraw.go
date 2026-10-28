package service

import (
	"context"
	"errors"
	"time"

	"github.com/ohstr/nmilat/nip01"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashwithdraw"
	"github.com/flokiorg/lokihub/hubclock"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

// withdrawRetentionInterval is how often withdraw records past their retention
// are dropped (the cash record job's period).
const withdrawRetentionInterval = 5 * time.Minute

// withdrawPruneBatch bounds one retention pass.
const withdrawPruneBatch = 500

// setupWithdraw builds the on-chain withdrawal service and hands it to the
// private dispatch, and starts its record retention. Without a payout engine
// (Funds) no hub offers withdrawals; committed payouts are still answered.
func (svc *service) setupWithdraw() {
	svc.withdraw = &cashwithdraw.Service{
		DB:     svc.db,
		Cfg:    svc.cfg,
		Keys:   svc.keys,
		Apps:   apps.NewAppsService(svc.db, svc.eventPublisher, svc.keys, svc.cfg),
		Events: svc.eventPublisher,
		LN:     func() lnclient.LNClient { return svc.lnClient },
		IA:     apps.NewIdentityAuthorityManager(svc.db),
	}
	// A Cash Hub stays while a payout of it is owed or its record is kept.
	apps.RegisterCashHubDeleteBlocker("cash_withdraw", svc.withdraw.HubDeleteBlockers)
	// The payout engine is the withdrawal service's Funds; it runs for the
	// app's life and idles while the node lacks the on-chain capability.
	svc.withdrawEngine = cashwithdraw.NewEngine(svc.withdraw)
	svc.withdrawEngine.Hooks = withdrawEngineHooks()
	svc.withdrawEngine.Notices = poolNoticeSender{svc: svc}
	if w, ok := svc.nip47Service.(interface{ SetWithdraw(*cashwithdraw.Service) }); ok {
		w.SetWithdraw(svc.withdraw)
	}
	if svc.ctx == nil {
		return
	}
	go svc.withdrawEngine.Run(svc.ctx)
	go func() {
		ticker := time.NewTicker(withdrawRetentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-svc.ctx.Done():
				return
			case <-ticker.C:
			case <-hubclock.Moved():
				// The clock was moved (a regtest test hook): retention is due now.
			}
			if _, err := svc.withdraw.PruneRecords(withdrawPruneBatch); err != nil {
				logger.Logger.Error().Err(err).Msg("Failed to drop withdraw records past their retention")
			}
		}
	}()
}

// GetWithdrawService is the on-chain withdrawal service.
func (svc *service) GetWithdrawService() *cashwithdraw.Service { return svc.withdraw }

// poolNoticeSender publishes a payout's state notice the way an order notice is
// published (publishNotice): it is NIP-CO's order notice, sent on the call's
// arrival relay and on the Hub's other relays only if that one refuses. The
// pool is the one Start opens; until then no notice is due yet in practice,
// and one that is gets a refusal (clients poll).
type poolNoticeSender struct{ svc *service }

func (s poolNoticeSender) SendNotice(ctx context.Context, arrival string, ev *nip01.Event) error {
	pool := s.svc.noticePool.Load()
	if pool == nil {
		return errors.New("the relay pool is not open yet")
	}
	return s.svc.publishNotice(ctx, pool, arrival, ev)
}

// GetWithdrawEngine is the payout engine.
func (svc *service) GetWithdrawEngine() *cashwithdraw.Engine { return svc.withdrawEngine }
