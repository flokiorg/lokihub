// Package ledger writes balance movements between this node's apps directly as
// ledger rows, inside a caller's transaction, with no Lightning call.
package ledger

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// InternalTransferTx moves amountMillis from fromAppID's isolated balance to
// toAppID's inside tx: one SETTLED outgoing row and one SETTLED incoming row,
// fee 0, no fee reserve, sharing a synthetic payment hash
// sha256("lokihub-internal:" ‖ 16 random bytes).
//
// No invoice exists on the node for that hash, so nothing outside this node can
// ever pay it; isolated balances are sums over these rows, so the two rows are
// the whole transfer. Being plain rows, the transfer commits or rolls back with
// tx, which is what lets a cash call commit as one transaction (NIP-CASH
// §Atomicity — One Commit). It refuses to overdraw fromAppID.
func InternalTransferTx(tx *gorm.DB, fromAppID, toAppID uint, amountMillis uint64, purpose string) error {
	if amountMillis == 0 {
		return fmt.Errorf("%w: an internal transfer moves a positive amount", constants.ErrInvalidParams)
	}
	if amountMillis > math.MaxInt64 {
		return fmt.Errorf("%w: amount %d is too large", constants.ErrInvalidParams, amountMillis)
	}
	if fromAppID == toAppID {
		return fmt.Errorf("%w: an internal transfer needs two different apps", constants.ErrInvalidParams)
	}
	if queries.GetIsolatedBalance(tx, fromAppID) < int64(amountMillis) { //nolint:gosec // bounded above
		return fmt.Errorf("%w: app %d cannot fund %d", transactions.NewInsufficientBalanceError(), fromAppID, amountMillis)
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("internal transfer nonce: %w", err)
	}
	hash := sha256.Sum256(append([]byte("lokihub-internal:"), nonce...))
	paymentHash := hex.EncodeToString(hash[:])
	preimage := hex.EncodeToString(nonce)
	meta, err := json.Marshal(map[string]interface{}{"internal_transfer": true, "purpose": purpose})
	if err != nil {
		return err
	}
	now := time.Now()
	from, to := fromAppID, toAppID
	rows := []db.Transaction{
		{
			AppId: &from, Type: constants.TRANSACTION_TYPE_OUTGOING, State: constants.TRANSACTION_STATE_SETTLED,
			AmountMloki: amountMillis, PaymentHash: paymentHash, Preimage: &preimage, Description: purpose,
			Metadata: datatypes.JSON(meta), SelfPayment: true, SettledAt: &now,
		},
		{
			AppId: &to, Type: constants.TRANSACTION_TYPE_INCOMING, State: constants.TRANSACTION_STATE_SETTLED,
			AmountMloki: amountMillis, PaymentHash: paymentHash, Preimage: &preimage, Description: purpose,
			Metadata: datatypes.JSON(meta), SelfPayment: true, SettledAt: &now,
		},
	}
	if err := tx.Create(&rows).Error; err != nil {
		return fmt.Errorf("record internal transfer: %w", err)
	}
	return nil
}
