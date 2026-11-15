package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/transactions"
)

// ErrDuplicateCredit: a credit under this key was already written.
var ErrDuplicateCredit = errors.New("an external credit under this key was already written")

// externalHash is the synthetic payment hash of an external movement keyed by
// key: no invoice exists for it, so nothing outside this node can pay it.
func externalHash(key string) string {
	sum := sha256.Sum256([]byte("lokihub-external:" + key))
	return hex.EncodeToString(sum[:])
}

func externalRow(appID uint, kind string, amountMillis uint64, purpose, key string, now time.Time) (db.Transaction, error) {
	meta, err := json.Marshal(map[string]interface{}{"external": true, "purpose": purpose, "key": key})
	if err != nil {
		return db.Transaction{}, err
	}
	id := appID
	hash := externalHash(key)
	return db.Transaction{
		AppId: &id, Type: kind, State: constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: amountMillis, PaymentHash: hash, Description: purpose,
		Metadata: datatypes.JSON(meta), SettledAt: &now,
	}, nil
}

func checkAmount(amountMillis uint64) error {
	if amountMillis == 0 {
		return fmt.Errorf("%w: an external movement moves a positive amount", constants.ErrInvalidParams)
	}
	if amountMillis > math.MaxInt64 {
		return fmt.Errorf("%w: amount %d is too large", constants.ErrInvalidParams, amountMillis)
	}
	return nil
}

// ExternalDebitTx moves amountMillis out of appID's isolated balance inside tx,
// to no other app: one SETTLED outgoing row keyed by key (for a cash_withdraw,
// the request_id), fee 0. The value is then held outside every app (a
// withdrawal's held_millis) until it is paid on-chain or re-credited. It refuses
// to overdraw appID.
func ExternalDebitTx(tx *gorm.DB, appID uint, amountMillis uint64, purpose, key string) error {
	if err := checkAmount(amountMillis); err != nil {
		return err
	}
	if queries.GetIsolatedBalance(tx, appID) < int64(amountMillis) { //nolint:gosec // bounded above
		return fmt.Errorf("%w: app %d cannot fund %d", transactions.NewInsufficientBalanceError(), appID, amountMillis)
	}
	row, err := externalRow(appID, constants.TRANSACTION_TYPE_OUTGOING, amountMillis, purpose, key, time.Now())
	if err != nil {
		return err
	}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("record external debit: %w", err)
	}
	return nil
}

// HeldCreditTx credits amountMillis to appID inside tx from value held
// outside every app (a cancelled withdrawal's hold): one SETTLED incoming row
// keyed by key. A second credit under the same key is ErrDuplicateCredit, so a
// retried credit never pays twice.
func HeldCreditTx(tx *gorm.DB, appID uint, amountMillis uint64, purpose, key string) error {
	if err := checkAmount(amountMillis); err != nil {
		return err
	}
	var n int64
	if err := tx.Model(&db.Transaction{}).
		Where("payment_hash = ? AND type = ?", externalHash(key), constants.TRANSACTION_TYPE_INCOMING).
		Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return ErrDuplicateCredit
	}
	row, err := externalRow(appID, constants.TRANSACTION_TYPE_INCOMING, amountMillis, purpose, key, time.Now())
	if err != nil {
		return err
	}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("record external credit: %w", err)
	}
	return nil
}
