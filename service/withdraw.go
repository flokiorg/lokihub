package service

import (
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashwithdraw"
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
	if w, ok := svc.nip47Service.(interface{ SetWithdraw(*cashwithdraw.Service) }); ok {
		w.SetWithdraw(svc.withdraw)
	}
	if svc.ctx == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(withdrawRetentionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-svc.ctx.Done():
				return
			case <-ticker.C:
				if _, err := svc.withdraw.PruneRecords(withdrawPruneBatch); err != nil {
					logger.Logger.Error().Err(err).Msg("Failed to drop withdraw records past their retention")
				}
			}
		}
	}()
}

// GetWithdrawService is the on-chain withdrawal service.
func (svc *service) GetWithdrawService() *cashwithdraw.Service { return svc.withdraw }
