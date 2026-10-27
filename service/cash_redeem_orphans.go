package service

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

// orphanRedeemGrace is how long a redeem claim may go without a payment row
// before it is taken for orphaned. The row is written within moments of the
// claim, so this only has to outlast a slow call.
const orphanRedeemGrace = 10 * time.Minute

// StartOrphanRedeemSweep resolves, once the node is up and then with the cash
// cleanup's cadence, the redeem claims a restart or a failed write left with no
// pending payment and no preimage (apps.ResolveOrphanRedeemClaims). Without it
// such a slice stays claimed, and holds one of the in-flight slots, for good.
func StartOrphanRedeemSweep(ctx context.Context, gormDB *gorm.DB, appsService apps.AppsService, getLNClient func() lnclient.LNClient, eventPublisher events.EventPublisher) {
	go func() {
		ticker := time.NewTicker(cashCleanupInterval)
		defer ticker.Stop()
		for {
			if lnClient := getLNClient(); lnClient != nil {
				ResolveOrphanRedeems(ctx, gormDB, appsService, lnClient, eventPublisher, time.Now())
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// ResolveOrphanRedeems is one run of the orphan sweep at now.
func ResolveOrphanRedeems(ctx context.Context, gormDB *gorm.DB, appsService apps.AppsService, lnClient lnclient.LNClient, eventPublisher events.EventPublisher, now time.Time) {
	var lookup apps.PayoutLookup
	if looker, ok := lnClient.(lnclient.PaymentLooker); ok {
		lookup = func(paymentHash string) (apps.PayoutState, string, uint64) {
			res, err := looker.LookupPayment(ctx, paymentHash)
			if err != nil {
				return apps.PayoutUnknown, "", 0
			}
			switch res.Status {
			case lnclient.PaymentStatusSucceeded:
				return apps.PayoutSucceeded, res.Preimage, res.FeeMloki
			case lnclient.PaymentStatusFailed:
				return apps.PayoutFailed, "", 0
			}
			return apps.PayoutUnknown, "", 0
		}
	}
	settledOn, err := appsService.ResolveOrphanRedeemClaims(now.Add(-orphanRedeemGrace), lookup)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to resolve orphaned cash_redeem claims")
	}
	for _, walletAppID := range settledOn {
		if _, err := cashwallet.AutoDeleteDrained(gormDB, eventPublisher, walletAppID); err != nil {
			logger.Logger.Error().Err(err).Uint("app_id", walletAppID).
				Msg("Failed to archive and auto-delete a Cash wallet drained by a resolved redeem")
		}
	}
}
