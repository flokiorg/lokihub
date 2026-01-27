package persist

import (
	"time"
)

// LSPS1Order represents a persistent LSPS1 order (inbound liquidity request).
type LSPS1Order struct {
	OrderID        string    `gorm:"primaryKey" json:"order_id"`
	LSPPubkey      string    `gorm:"index" json:"lsp_pubkey"`
	State          string    `json:"state"` // CREATED, COMPLETED, FAILED, etc.
	PaymentInvoice string    `json:"payment_invoice"`
	FeeTotal       uint64    `json:"fee_total"`
	OrderTotal     uint64    `json:"order_total"`
	LSPBalance     uint64    `json:"lsp_balance_loki"`
	ClientBalance  uint64    `json:"client_balance_loki"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ExpiresAt      time.Time `json:"expires_at"` // Optional connection to channel expiry or payment expiry
}

// TableName overrides the table name to 'lsps1_orders'
func (LSPS1Order) TableName() string {
	return "lsps1_orders"
}
