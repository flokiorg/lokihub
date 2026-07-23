package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/transactions"
	"gorm.io/gorm"
)

const jitCleanupInterval = 5 * time.Minute

// StartJITCleanupService runs a background goroutine that periodically reclaims
// funds from expired JIT and circle_child sub-wallets back to their parent app.
// getLNClient is called each tick so the service works even when the client
// starts after the goroutine is launched.
func StartJITCleanupService(ctx context.Context, gormDB *gorm.DB, transactionsSvc transactions.TransactionsService, getLNClient func() lnclient.LNClient) {
	go func() {
		ticker := time.NewTicker(jitCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				lnClient := getLNClient()
				if lnClient == nil {
					continue
				}
				runJITCleanup(ctx, gormDB, transactionsSvc, lnClient)
				transactionsSvc.SweepStalePendingOutgoing(ctx, lnClient)
				pruneStaleCircleWalletIdentityProofs(gormDB)
			}
		}
	}()
}

// cleanupBatchSize bounds memory per cleanup tick: 200 × ~400 B ≈ 80 KB max.
const cleanupBatchSize = 200

// maxBatchesPerTick bounds total work per tick (up to 2000 apps) so a large
// backlog spreads across multiple 5-minute ticks instead of monopolizing this
// goroutine and delaying SweepStalePendingOutgoing indefinitely. This also
// guards against a query that keeps re-matching the same rows: an app with a
// pending incoming payment is deferred (see ReclaimAndDeleteSubWallet) without being
// deleted or flagged, so it stays in the result set until the payment settles
// or expires — with no cap, enough simultaneously-deferred apps would spin
// this loop forever within a single tick.
const maxBatchesPerTick = 10

func runJITCleanup(ctx context.Context, gormDB *gorm.DB, transactionsSvc transactions.TransactionsService, lnClient lnclient.LNClient) {
	for batchNum := 0; batchNum < maxBatchesPerTick; batchNum++ {
		var batch []db.App
		err := gormDB.Where(
			"parent_app_id IS NOT NULL AND expires_at < ? AND cleanup_in_progress = ?",
			time.Now(), false,
		).Limit(cleanupBatchSize).Find(&batch).Error
		if err != nil {
			logger.Logger.Error().Err(err).Msg("JIT cleanup: failed to query expired sub-wallets")
			return
		}
		if len(batch) == 0 {
			return
		}
		for _, app := range batch {
			if err := ReclaimAndDeleteSubWallet(ctx, gormDB, transactionsSvc, lnClient, app); err != nil {
				if errors.Is(err, constants.ErrInvalidParams) {
					// Expected, transient deferral (pending incoming settlement, or
					// already claimed by a concurrent tick/manual delete) — the next
					// tick retries, so this isn't worth an error-level log.
					logger.Logger.Debug().Err(err).Uint("app_id", app.ID).Msg("JIT cleanup: deferring sub-wallet reclaim")
				} else {
					logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("JIT cleanup: failed to reclaim expired sub-wallet")
				}
			}
		}
		if len(batch) < cleanupBatchSize {
			return
		}
		if batchNum == maxBatchesPerTick-1 {
			logger.Logger.Warn().
				Int("batches_processed", maxBatchesPerTick).
				Msg("JIT cleanup: per-tick batch cap reached, remaining backlog will continue on the next tick")
		}
	}
}

// circleWalletIdentityProofRetention bounds how long consumed
// create_circle_wallet identity proof event IDs (see
// nip47/controllers/create_circle_wallet_identity.go) are kept for replay
// detection. Well beyond the 5-minute proof freshness window, so a proof can
// never become reusable before its row is pruned.
const circleWalletIdentityProofRetention = time.Hour

// pruneStaleCircleWalletIdentityProofs deletes replay-guard rows old enough
// that their proofs could no longer pass the freshness check anyway, keeping
// the table from growing unbounded. Piggybacks on the existing JIT cleanup
// ticker rather than running its own goroutine.
func pruneStaleCircleWalletIdentityProofs(gormDB *gorm.DB) {
	if err := gormDB.Where("created_at < ?", time.Now().Add(-circleWalletIdentityProofRetention)).
		Delete(&db.CircleWalletIdentityProof{}).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("JIT cleanup: failed to prune stale circle wallet identity proofs")
	}
}

// ReclaimAndDeleteSubWallet reclaims any remaining isolated balance of a
// jit_wallet/circle_wallet child back to its parent app via an internal
// transfer, then deletes the child app. Shared by the periodic expiry-cleanup
// ticker (runJITCleanup) and manual "delete this wallet" admin actions
// (api.DeleteJITHubAllocation, api.DeleteCircleWalletChild) — in both cases
// funds must never be silently destroyed or stranded, and the two callers
// must not be able to double-process the same wallet concurrently.
//
// Errors wrapped in constants.ErrInvalidParams are expected, transient
// deferrals (a payment still settling in, or another caller already
// reclaiming this wallet) that callers may retry; any other error is a real
// failure.
func ReclaimAndDeleteSubWallet(ctx context.Context, gormDB *gorm.DB, transactionsSvc transactions.TransactionsService, lnClient lnclient.LNClient, app db.App) error {
	if app.ParentAppID == nil {
		return fmt.Errorf("app %d has no parent app to reclaim balance into", app.ID)
	}

	// A payment may still be settling into this wallet (e.g. an invoice created
	// just before expiry/deletion). Deleting the app now would cascade-delete that
	// pending transaction, and the settlement would then have nowhere to credit —
	// silently losing funds. Defer until nothing is in flight; the caller retries.
	if queries.HasPendingIncoming(gormDB, app.ID) {
		return fmt.Errorf("%w: a payment is still settling into this wallet, try again shortly", constants.ErrInvalidParams)
	}

	// Atomically claim the cleanup slot (acts as a mutex per app row) so a
	// concurrent expiry-cleanup tick and a manual delete can't both process
	// the same wallet.
	result := gormDB.Model(&db.App{}).
		Where("id = ? AND cleanup_in_progress = ?", app.ID, false).
		Update("cleanup_in_progress", true)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: wallet is already being reclaimed", constants.ErrInvalidParams)
	}

	balance := queries.GetIsolatedBalance(gormDB, app.ID)
	writtenOff := false
	if balance > 0 {
		// The parent may itself have been deleted (e.g. a jit_hub/circle_hub
		// removed via a path that predates the child-count guard in
		// apps.DeleteApp, or a manually edited DB). Inserting a reclaim
		// transaction against a nonexistent parent_app_id would violate the
		// apps FK on every retry, forever. There's no valid destination for
		// the funds in that case, so write off the balance and let the
		// deletion below proceed instead of retry-looping.
		var parentExists int64
		if err := gormDB.Model(&db.App{}).Where("id = ?", *app.ParentAppID).Count(&parentExists).Error; err != nil {
			gormDB.Model(&db.App{}).Where("id = ?", app.ID).Update("cleanup_in_progress", false)
			return err
		}
		if parentExists == 0 {
			writtenOff = true
			logger.Logger.Error().Uint("app_id", app.ID).Uint("parent_app_id", *app.ParentAppID).
				Uint64("balance_mloki", uint64(balance)). //nolint:gosec // guarded by the balance > 0 check above
				Msg("JIT cleanup: parent app no longer exists, sub-wallet balance cannot be reclaimed and is being written off")
		} else {
			invoice, err := transactionsSvc.MakeInvoice(
				ctx, uint64(balance), "jit cleanup", "", 0, //nolint:gosec // guarded by the balance > 0 check above
				nil, lnClient, app.ParentAppID, nil, nil, nil, nil, nil, nil,
				&transactions.InternalMakeInvoiceMeta{InternalTransfer: true},
			)
			if err != nil {
				gormDB.Model(&db.App{}).Where("id = ?", app.ID).Update("cleanup_in_progress", false)
				return fmt.Errorf("failed to create reclaim invoice: %w", err)
			}

			_, err = transactionsSvc.SendPaymentSync(
				invoice.PaymentRequest, nil,
				map[string]interface{}{"internal_transfer": true},
				lnClient, &app.ID, nil,
			)
			if err != nil {
				gormDB.Model(&db.App{}).Where("id = ?", app.ID).Update("cleanup_in_progress", false)
				return fmt.Errorf("failed to transfer balance back to parent: %w", err)
			}
		}
	}

	if err := gormDB.Delete(&db.App{}, app.ID).Error; err != nil {
		return fmt.Errorf("failed to delete sub-wallet: %w", err)
	}
	if writtenOff {
		logger.Logger.Info().Uint("app_id", app.ID).Uint("parent_app_id", *app.ParentAppID).Msg("sub-wallet balance written off and app deleted")
	} else {
		logger.Logger.Info().Uint("app_id", app.ID).Uint("parent_app_id", *app.ParentAppID).Msg("sub-wallet balance reclaimed and app deleted")
	}
	return nil
}
