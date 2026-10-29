package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/flokiorg/lokihub/hubclock"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/circlewallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/transactions"
)

// circleLifecycleInterval is how often the Circle Wallet lifecycle job runs;
// circleRotationInterval how often due rotations are completed.
const (
	circleLifecycleInterval = 60 * time.Second
	circleRotationInterval  = 10 * time.Second
)

// RunCircleRotations completes every rotation that has come due (NIP-CW
// §Recovering a Lost Connection), or cancels it when its new key got used, and
// sends the notifications.
func RunCircleRotations(gormDB *gorm.DB, eventPublisher events.EventPublisher, now time.Time) {
	notices, swapped, err := circlelife.CompleteDueRotations(gormDB, now, nil)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Circle rotations: failed to complete due rotations")
		return
	}
	if len(swapped) > 0 {
		logger.Logger.Info().Int("rotated", len(swapped)).Msg("Circle rotations: completed")
	}
	circlelife.Publish(eventPublisher, notices)
}

// StartCircleLifecycleService runs the Circle Wallet lifecycle job (NIP-CW
// §Lifecycle and Deletion) every minute.
func StartCircleLifecycleService(ctx context.Context, gormDB *gorm.DB, transactionsSvc transactions.TransactionsService,
	getLNClient func() lnclient.LNClient, eventPublisher events.EventPublisher) {
	go func() {
		ticker := time.NewTicker(circleRotationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				RunCircleRotations(gormDB, eventPublisher, hubclock.Now())
			case <-hubclock.Moved():
				RunCircleRotations(gormDB, eventPublisher, hubclock.Now())
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(circleLifecycleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				RunCircleLifecycle(ctx, gormDB, transactionsSvc, getLNClient(), eventPublisher, hubclock.Now())
			case <-hubclock.Moved():
				RunCircleLifecycle(ctx, gormDB, transactionsSvc, getLNClient(), eventPublisher, hubclock.Now())
			}
		}
	}()
}

// CircleLifecycleResult counts what one run did, for the host's log.
type CircleLifecycleResult struct {
	Expired, Closed, Deleted, HubsDeleted int
}

// RunCircleLifecycle is one run of the lifecycle job at now: Active or
// Suspended wallets past their expiry become Expired; Suspended wallets past
// their grace become Closing; Expired or Closing wallets past their grace that
// hold at most their dust and have nothing in flight are deleted, their dust
// kept by the Hub; every wallet that is not Active has its open invoices
// cancelled; and a torn-down Hub with no wallet left goes. A wallet holding
// more than dust is never deleted, whatever its state.
func RunCircleLifecycle(ctx context.Context, gormDB *gorm.DB, transactionsSvc transactions.TransactionsService,
	lnClient lnclient.LNClient, eventPublisher events.EventPublisher, now time.Time) CircleLifecycleResult {
	var result CircleLifecycleResult
	var wallets []db.App
	if err := gormDB.Where("kind = ?", db.AppKindCircleWallet).Find(&wallets).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Circle lifecycle: failed to list circle wallets")
		return result
	}
	for i := range wallets {
		w := &wallets[i]
		stored := circlelife.State(w)
		switch {
		case (stored == circlelife.StateActive || stored == circlelife.StateSuspended) && circlelife.EffectiveState(w, now) == circlelife.StateExpired:
			if transitionWallet(gormDB, w, circlelife.EventExpire, now) {
				result.Expired++
			}
		case stored == circlelife.StateSuspended && w.CircleGraceEndsAt != nil && now.Unix() >= *w.CircleGraceEndsAt:
			if transitionWallet(gormDB, w, circlelife.EventGraceEnd, now) {
				result.Closed++
			}
		}
		if circlelife.IsActive(w, now) {
			continue
		}
		if lnClient != nil {
			if err := transactionsSvc.CancelOpenCircleInvoices(ctx, w.ID, lnClient); err != nil {
				logger.Logger.Error().Err(err).Uint("app_id", w.ID).Msg("Circle lifecycle: failed to cancel a wallet's open invoices; retrying next run")
				continue
			}
		}
		state := circlelife.State(w)
		if (state == circlelife.StateExpired || state == circlelife.StateClosing) && w.CircleGraceEndsAt != nil && now.Unix() >= *w.CircleGraceEndsAt {
			err := gormDB.Transaction(func(tx *gorm.DB) error {
				var current db.App
				if err := tx.Where("id = ?", w.ID).First(&current).Error; err != nil {
					return err
				}
				return circlewallet.DeleteWallet(tx, &current, now)
			})
			switch {
			case errors.Is(err, circlewallet.ErrNotDeletable):
				// Holds more than dust or has a payment in flight: it stays,
				// paying out only, for as long as it does.
			case err != nil:
				logger.Logger.Error().Err(err).Uint("app_id", w.ID).Msg("Circle lifecycle: failed to delete a wallet")
			default:
				result.Deleted++
				publishAppDeleted(eventPublisher, w.ID)
			}
		}
	}
	result.HubsDeleted = deleteTornDownHubs(gormDB, eventPublisher)
	if result != (CircleLifecycleResult{}) {
		logger.Logger.Info().Interface("result", result).Msg("Circle lifecycle: run done")
	}
	return result
}

// transitionWallet applies event to w under its Hub's lock, reporting whether
// it moved.
func transitionWallet(gormDB *gorm.DB, w *db.App, event circlelife.Event, now time.Time) bool {
	err := gormDB.Transaction(func(tx *gorm.DB) error {
		if err := db.LockApp(tx, *w.ParentAppID); err != nil {
			return err
		}
		if err := tx.Where("id = ?", w.ID).First(w).Error; err != nil {
			return err
		}
		var cfg db.CircleHubConfig
		if err := tx.Where("app_id = ?", *w.ParentAppID).First(&cfg).Error; err != nil {
			return err
		}
		return circlelife.Transition(tx, w, event, now, cfg.GraceSecs)
	})
	if err != nil && !errors.Is(err, circlelife.ErrForbiddenEdge) {
		logger.Logger.Error().Err(err).Uint("app_id", w.ID).Str("event", event.String()).Msg("Circle lifecycle: failed to move a wallet")
	}
	return err == nil
}

// deleteTornDownHubs deletes every torn-down Circle Wallet Hub whose last wallet
// is gone (NIP-CW §Lifecycle and Deletion, Hub teardown). Its own balance, no
// longer isolated, is the host's again.
func deleteTornDownHubs(gormDB *gorm.DB, eventPublisher events.EventPublisher) int {
	var hubIDs []uint
	if err := gormDB.Model(&db.CircleHubConfig{}).Where("teardown_requested_at IS NOT NULL").Pluck("app_id", &hubIDs).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Circle lifecycle: failed to list torn-down hubs")
		return 0
	}
	deleted := 0
	for _, hubID := range hubIDs {
		gone := false
		err := gormDB.Transaction(func(tx *gorm.DB) error {
			if err := db.LockApp(tx, hubID); err != nil {
				return err
			}
			var children int64
			if err := tx.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", hubID, db.AppKindCircleWallet).Count(&children).Error; err != nil {
				return err
			}
			if children > 0 {
				return nil
			}
			if err := tx.Delete(&db.App{}, hubID).Error; err != nil {
				return err
			}
			gone = true
			return nil
		})
		if err != nil {
			logger.Logger.Error().Err(err).Uint("app_id", hubID).Msg("Circle lifecycle: failed to delete a torn-down hub")
			continue
		}
		if gone {
			deleted++
			publishAppDeleted(eventPublisher, hubID)
		}
	}
	return deleted
}

func publishAppDeleted(eventPublisher events.EventPublisher, appID uint) {
	if eventPublisher == nil {
		return
	}
	eventPublisher.Publish(&events.Event{Event: "nwc_app_deleted", Properties: map[string]interface{}{"id": appID}})
}

// errCircleWalletNeverSwept is the cash cleanup's refusal for a Circle Wallet:
// a member's balance is never swept (NIP-CW §Lifecycle and Deletion).
var errCircleWalletNeverSwept = fmt.Errorf("%w: a circle wallet is never swept; it is closed and deleted by its lifecycle", constants.ErrInvalidParams)
