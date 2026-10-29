package cashrequest

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// Methods whose records another component retires: an order's record follows
// NIP-CO retention, a withdrawal's follows its payout. Named here until nmilat
// exports MethodCashOrder and MethodCashWithdraw.
var ownedElsewhere = []string{"cash_order", "cash_withdraw"}

// OnBillArchivedTx runs when a bill is archived, inside the archiving
// transaction and after its app row is deleted: every record that created it and has no live bill left gets
// drop_after = max(deadline + H + 1, latest retained_until + 1), so the record
// outlives every instant a lookup could still answer done or a tombstone answers
// (NIP-CASH §The Record).
func OnBillArchivedTx(tx *gorm.DB, walletAppID uint) error {
	recs, err := lockRecordsOf(tx, walletAppID)
	if err != nil {
		return fmt.Errorf("records of bill %d: %w", walletAppID, err)
	}
	if len(recs) == 0 {
		return nil
	}
	h, err := HorizonTx(tx)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		var bills []db.CashRequestRecordBill
		if err := tx.Where("record_id = ?", rec.ID).Find(&bills).Error; err != nil {
			return err
		}
		dropAfter := rec.Deadline + h + 1
		allGone := true
		for _, b := range bills {
			var live int64
			if err := tx.Model(&db.App{}).Where("id = ?", b.WalletAppID).Count(&live).Error; err != nil {
				return err
			}
			if live > 0 {
				allGone = false
				break
			}
			var archived db.CashBillArchive
			err := tx.Where("wallet_app_id = ?", b.WalletAppID).Limit(1).Find(&archived).Error
			if err != nil {
				return err
			}
			if archived.RetainedUntil != nil {
				dropAfter = max(dropAfter, archived.RetainedUntil.Unix()+1)
			}
		}
		if !allGone {
			continue
		}
		if err := tx.Model(&db.CashRequestRecord{}).Where("id = ?", rec.ID).Update("drop_after", dropAfter).Error; err != nil {
			return err
		}
	}
	return nil
}

// PruneRecords deletes records whose drop_after has passed, at most limit at a
// time, leaving the records another component owns. It never touches the used
// marks, which are permanent.
func (s Store) PruneRecords(limit int) (int64, error) {
	now := s.now().Unix()
	ids := s.DB.Model(&db.CashRequestRecord{}).Select("id").
		Where("drop_after IS NOT NULL AND drop_after <= ? AND method NOT IN ?", now, ownedElsewhere).
		Limit(limit)
	res := s.DB.Where("id IN (?)", ids).Delete(&db.CashRequestRecord{})
	return res.RowsAffected, res.Error
}

// ProofRetentionSecs is how long a consumed claim proof is kept past its
// created_at, well past the 6 minutes NIP-CASH requires (nipcash.ClaimRetainUntil).
const ProofRetentionSecs = 3600

// PruneProofs deletes consumed proofs that could no longer pass the freshness check.
func (s Store) PruneProofs() (int64, error) {
	res := s.DB.Where("proof_created_at < ?", s.now().Unix()-ProofRetentionSecs).Delete(&db.CashConsumedProof{})
	return res.RowsAffected, res.Error
}
