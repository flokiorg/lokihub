package apps

import (
	"time"

	"github.com/flokiorg/lokihub/db"
)

// RecordCashStrandedFund durably logs one compensating-saga reversal that
// itself failed — see db.CashStrandedFund's own doc comment. Best-effort:
// callers log the returned error alongside their own, but must never let it
// change saga control flow (the funds are already safely stranded in a live,
// undeleted wallet app either way; this call only affects how discoverable
// that fact is).
func (svc *appsService) RecordCashStrandedFund(operation string, sourceWalletAppID, retainedWalletAppID uint, amountMloki uint64) error {
	return svc.db.Create(&db.CashStrandedFund{
		Operation:           operation,
		SourceWalletAppID:   sourceWalletAppID,
		RetainedWalletAppID: retainedWalletAppID,
		AmountMloki:         amountMloki,
	}).Error
}

// ListCashStrandedFunds returns every CashStrandedFund record, newest first.
func (svc *appsService) ListCashStrandedFunds(onlyUnresolved bool) ([]db.CashStrandedFund, error) {
	var records []db.CashStrandedFund
	query := svc.db.Order("created_at DESC")
	if onlyUnresolved {
		query = query.Where("resolved_at IS NULL")
	}
	if err := query.Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

// ResolveCashStrandedFund sets ResolvedAt on one record. Idempotent: a
// record that's already resolved (or a missing ID) is a no-op, not an error
// — an operator retrying the same sweep-confirmation action shouldn't fail.
func (svc *appsService) ResolveCashStrandedFund(id uint) error {
	return svc.db.Model(&db.CashStrandedFund{}).
		Where("id = ? AND resolved_at IS NULL", id).
		Update("resolved_at", time.Now()).Error
}
