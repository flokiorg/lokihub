package ledger

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// ExternalCredit is money that arrived from outside the node's ledger (an order
// payment): one SETTLED incoming row on AppID carrying the payment's own hash,
// the invoice's payment hash for Lightning, a hash of the outpoint on chain.
type ExternalCredit struct {
	AppID       uint
	Amount      uint64
	PaymentHash string
	Invoice     string
	Description string
	Purpose     string
	Key         string // the payment's key: ln:<hash> or oc:<txid>:<vout>
}

// ExternalCreditTx writes c inside tx. The row is what funds the bills minted
// from the payment in the same transaction, so the app's own balance is never
// needed (NIP-CO §Minting).
func ExternalCreditTx(tx *gorm.DB, c ExternalCredit) (*db.Transaction, error) {
	if c.Amount == 0 || c.Amount > math.MaxInt64 {
		return nil, fmt.Errorf("%w: an external credit of %d", constants.ErrInvalidParams, c.Amount)
	}
	meta, err := json.Marshal(map[string]interface{}{"external_credit": true, "purpose": c.Purpose, "payment_key": c.Key})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	app := c.AppID
	row := db.Transaction{
		AppId: &app, Type: constants.TRANSACTION_TYPE_INCOMING, State: constants.TRANSACTION_STATE_SETTLED,
		AmountMloki: c.Amount, PaymentHash: c.PaymentHash, PaymentRequest: c.Invoice, Description: c.Description,
		Metadata: datatypes.JSON(meta), SettledAt: &now,
	}
	if err := tx.Create(&row).Error; err != nil {
		return nil, fmt.Errorf("record external credit: %w", err)
	}
	return &row, nil
}
