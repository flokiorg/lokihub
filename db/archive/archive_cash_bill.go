package archive

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// Package archive is the one place a cash bill's App row is destroyed.
//
// It is a leaf over db, importing nothing else of ours on purpose. Both apps
// and service must be able to reach it, and db/queries' own tests import the
// tests helper package, which imports apps — so anything apps depends on must
// stay clear of db/queries or that test binary cycles. Hence the balance read
// arrives as a function argument (see ArchiveAndDeleteDrainedCashBillTx)
// rather than as an import.

// ErrBillNotDrained reports that a bill is not eligible for auto-deletion: a
// slice is still unclaimed, or the balance is not zero. It is an expected
// outcome, not a failure — a bill in that state is simply still alive.
var ErrBillNotDrained = errors.New("cash bill is not fully drained")

// DrainGuards are the ledger reads the drain check needs. Callers pass
// queries.GetIsolatedBalance and queries.HasPendingIncoming; taking them as
// functions rather than importing them keeps this package a leaf (see the
// package comment) AND guarantees both reads happen on the caller's own
// transaction handle, which is the whole point of re-checking inside the
// transaction.
type DrainGuards struct {
	IsolatedBalance    func(tx *gorm.DB, appID uint) int64
	HasPendingIncoming func(tx *gorm.DB, appID uint) bool
}

// archiveSliceBatchSize matches CreateCashWalletClaimsTx's own batching.
const archiveSliceBatchSize = 50

// ArchiveAndDeleteCashBillTx archives one cash bill's history and hard-deletes
// its App row, atomically, inside tx.
//
// The hard delete is the point. An app the hub no longer has is answered with
// silence (nip47.HandleEvent), which is what makes a spent bill
// indistinguishable from a pubkey this hub never served — the anti-oracle
// property NIP-CASH §Lifecycle and Deletion relies on. The archive is what lets
// that stay true without destroying the operator's audit trail.
//
// tx MUST be a live transaction: archive-without-delete would leave a ghost
// bill still answering requests, and delete-without-archive is the silent data
// loss this whole table exists to prevent. Every read happens BEFORE the delete
// on purpose — db.CashWalletClaim and db.Transaction both cascade on the app
// (see their App fields), so afterwards there is nothing left to read.
//
// Idempotent: a bill whose App row is already gone returns nil, and the bill
// archive insert is ON CONFLICT DO NOTHING against its unique WalletAppID.
func ArchiveAndDeleteCashBillTx(tx *gorm.DB, app *db.App, outcome string, reclaimedMloki int64, now time.Time) error {
	// Re-read inside the transaction. On sqlite the _txlock=immediate setting
	// already serializes writers; on Postgres this takes the row lock that
	// makes two concurrent deleters resolve to one winner and one no-op.
	var live db.App
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&live, app.ID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil // another path already archived and deleted it
	}
	if err != nil {
		return fmt.Errorf("failed to re-read cash bill %d for archival: %w", app.ID, err)
	}

	// Ledger truth, read before the cascade destroys the transaction rows.
	var funded int64
	if err := tx.Raw(`
		SELECT COALESCE(SUM(amount_mloki), 0)
		FROM transactions
		WHERE app_id = ? AND type = ? AND state = ?`,
		live.ID, constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_SETTLED,
	).Scan(&funded).Error; err != nil {
		return fmt.Errorf("failed to read funded total for cash bill %d: %w", live.ID, err)
	}

	var claims []db.CashWalletClaim
	if err := tx.Where("wallet_app_id = ?", live.ID).Find(&claims).Error; err != nil {
		return fmt.Errorf("failed to read claims for cash bill %d: %w", live.ID, err)
	}

	if len(claims) > 0 {
		slices := make([]db.CashBillSliceArchive, 0, len(claims))
		for _, c := range claims {
			slices = append(slices, buildSliceArchive(&live, c, DeriveSliceOutcome(c, outcome), now))
		}
		if err := tx.CreateInBatches(slices, archiveSliceBatchSize).Error; err != nil {
			return fmt.Errorf("failed to archive slices of cash bill %d: %w", live.ID, err)
		}
	}

	// Summed from the archive, not from the claims above, so it includes any
	// slice archived earlier by an operator removing a single recipient — those
	// rows are already gone from cash_wallet_claims by now.
	var total int64
	if err := tx.Raw(
		`SELECT COALESCE(SUM(amount_mloki), 0) FROM cash_bill_slice_archives WHERE wallet_app_id = ?`,
		live.ID,
	).Scan(&total).Error; err != nil {
		return fmt.Errorf("failed to total archived slices of cash bill %d: %w", live.ID, err)
	}

	bill := db.CashBillArchive{
		WalletAppID:          live.ID,
		WalletPubkey:         derefString(live.WalletPubkey),
		MintedAt:             live.CreatedAt,
		EndedAt:              now,
		ExpiresAt:            live.ExpiresAt,
		Outcome:              outcome,
		TotalMloki:           total,
		FundedMloki:          funded,
		ReclaimedMloki:       reclaimedMloki,
		SplitFromWalletAppID: live.SplitFromWalletAppID,
	}
	if live.ParentAppID != nil {
		bill.HubAppID = *live.ParentAppID
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "wallet_app_id"}},
		DoNothing: true,
	}).Create(&bill).Error; err != nil {
		return fmt.Errorf("failed to archive cash bill %d: %w", live.ID, err)
	}

	// Resolving this inside the transaction rather than after the delete (as
	// the cleanup service used to, in a separate autocommit statement) means a
	// crash can no longer leave a resolved-in-spirit stranded-fund record open
	// against a wallet that no longer exists.
	if err := tx.Model(&db.CashStrandedFund{}).
		Where("retained_wallet_app_id = ? AND resolved_at IS NULL", live.ID).
		Update("resolved_at", now).Error; err != nil {
		return fmt.Errorf("failed to resolve stranded funds for cash bill %d: %w", live.ID, err)
	}

	if err := tx.Delete(&db.App{}, live.ID).Error; err != nil {
		return fmt.Errorf("failed to delete cash bill %d: %w", live.ID, err)
	}
	return nil
}

// ArchiveAndDeleteDrainedCashBillTx is ArchiveAndDeleteCashBillTx with the
// drain guard re-evaluated INSIDE tx: every slice claimed, and a real ledger
// balance of exactly zero, and nothing still settling into it. Returns
// ErrBillNotDrained when any of those does not hold.
//
// Evaluating the guard inside the transaction is the point. The check used to
// run with no transaction at all, so a concurrent UnclaimCashSlice rollback
// (cash_redeem_controller.go, when a payout fails) could restore an unclaimed
// slice between the check and the delete — destroying a bill that still backed
// live value. The balance check stays defensive too: a multi-recipient bill
// whose balance is unexpectedly nonzero is left for the expiry sweep rather
// than force-deleted.
func ArchiveAndDeleteDrainedCashBillTx(tx *gorm.DB, app *db.App, guards DrainGuards, now time.Time) error {
	var unclaimed int64
	if err := tx.Model(&db.CashWalletClaim{}).
		Where("wallet_app_id = ? AND claimed_at IS NULL", app.ID).
		Count(&unclaimed).Error; err != nil {
		return fmt.Errorf("failed to count unclaimed slices of cash bill %d: %w", app.ID, err)
	}
	if unclaimed > 0 {
		return ErrBillNotDrained
	}
	if balance := guards.IsolatedBalance(tx, app.ID); balance != 0 {
		return fmt.Errorf("%w: balance is %d, not zero", ErrBillNotDrained, balance)
	}
	// A settling incoming payment is invisible to the balance above, which
	// counts only SETTLED incoming. Deleting now would cascade that pending
	// transaction away and leave its settlement with nowhere to credit —
	// silently losing funds. Same guard the expiry sweep applies before it
	// reclaims (service.ReclaimAndDeleteSubWallet).
	if guards.HasPendingIncoming(tx, app.ID) {
		return fmt.Errorf("%w: a payment is still settling into it", ErrBillNotDrained)
	}
	return ArchiveAndDeleteCashBillTx(tx, app, db.CashBillOutcomeDrained, 0, now)
}

// ArchiveCashSliceTx archives one slice on its own, for an operator removing a
// single recipient from a bill that stays alive.
//
// Without this, that recipient would vanish with no record: the claim row is
// hard-deleted immediately, long before the bill itself is ever archived.
func ArchiveCashSliceTx(tx *gorm.DB, wallet *db.App, claim db.CashWalletClaim, outcome string, now time.Time) error {
	slice := buildSliceArchive(wallet, claim, outcome, now)
	if err := tx.Create(&slice).Error; err != nil {
		return fmt.Errorf("failed to archive slice %d of cash bill %d: %w", claim.ID, wallet.ID, err)
	}
	return nil
}

// DeriveSliceOutcome maps one claim to its terminal status.
//
// Derived per slice, never copied down from the bill: a single bill can hold
// one slice that was redeemed and another that was split away, and the bill's
// own outcome ("drained") is not even a member of the slice vocabulary. The
// bill outcome only supplies the terminal value for slices that were still
// UNCLAIMED when it died.
func DeriveSliceOutcome(claim db.CashWalletClaim, billOutcome string) string {
	if claim.ClaimedAt != nil {
		// ClaimedAt is set by exactly two things: ClaimCashSlice (a Lightning
		// payout) and SplitCashSliceAmount's full-split branch (value moved
		// into another bill, which records SpunOffToWalletAppID). A failed
		// payout is always rolled back by UnclaimCashSlice, so a claimed slice
		// with no spin-off target really was paid out — even if the
		// best-effort payment-fact write did not land.
		if claim.SpunOffToWalletAppID != nil {
			return db.CashSliceStatusSplit
		}
		return db.CashSliceStatusRedeemed
	}
	switch billOutcome {
	case db.CashBillOutcomeExpired:
		return db.CashSliceStatusExpired
	case db.CashBillOutcomeWrittenOff:
		return db.CashSliceStatusWrittenOff
	case db.CashBillOutcomeVoid:
		return db.CashSliceStatusVoid
	default:
		// "deleted" and "drained": the value went back to the hub, but the
		// slice's own window had not passed, so this is not "expired".
		return db.CashSliceStatusReclaimed
	}
}

func buildSliceArchive(wallet *db.App, c db.CashWalletClaim, outcome string, now time.Time) db.CashBillSliceArchive {
	slice := db.CashBillSliceArchive{
		WalletAppID:          wallet.ID,
		ClaimID:              c.ID,
		IdentityType:         c.IdentityType,
		IdentityValue:        c.IdentityValue,
		IAPubkey:             c.IAPubkey,
		AmountMloki:          c.AmountMloki,
		Outcome:              outcome,
		CreatedAt:            c.CreatedAt,
		ClaimedAt:            c.ClaimedAt,
		ArchivedAt:           now,
		WalletPubkey:         derefString(wallet.WalletPubkey),
		WalletExpiresAt:      wallet.ExpiresAt,
		MinTransferMloki:     c.MinTransferMloki,
		RedeemFeePpm:         c.RedeemFeePpm,
		PaymentHash:          c.PaymentHash,
		Preimage:             c.Preimage,
		RedeemFeeMloki:       c.RedeemFeeMloki,
		RoutingFeeMloki:      c.RoutingFeeMloki,
		SettledAt:            c.SettledAt,
		SpunOffToWalletAppID: c.SpunOffToWalletAppID,
	}
	if wallet.ParentAppID != nil {
		slice.HubAppID = *wallet.ParentAppID
	}
	return slice
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
