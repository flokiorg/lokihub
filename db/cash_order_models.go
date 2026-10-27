package db

// AppKindCashOrder is an Order connection (NIP-CO §The Order Connection): a
// child of a Cash Hub that sells its cash. It holds no balance and can spend
// nothing; its pairing key is re-derived from its app id, as a bill's is.
const AppKindCashOrder = "cash_order"

// CashOrderConnection is an Order connection's own row beside its app: its
// label and when it was retired (irreversible). It lives as long as its Hub.
type CashOrderConnection struct {
	AppID     uint   `gorm:"primaryKey;autoIncrement:false"`
	App       App    `gorm:"foreignKey:AppID;constraint:OnDelete:CASCADE"`
	HubAppID  uint   `gorm:"not null;index"`
	Label     string `gorm:"size:255"`
	RetiredAt *int64
	CreatedAt int64 `gorm:"not null"`
}

// CashOrderTerms are a Cash Hub's selling terms (NIP-CO §Terms) and its open-order
// limits. TermsJSON is the nipCO.Terms object get_info reports.
type CashOrderTerms struct {
	HubAppID                 uint   `gorm:"primaryKey;autoIncrement:false"`
	Hub                      App    `gorm:"foreignKey:HubAppID;constraint:OnDelete:CASCADE"`
	TermsJSON                string `gorm:"type:text;not null"`
	OpenCeilingLnMillis      int64  `gorm:"not null;default:0"`
	OpenCeilingOnchainMillis int64  `gorm:"not null;default:0"`
	OpenOnchainMax           int    `gorm:"not null;default:0"`
	UpdatedAt                int64  `gorm:"not null"`
}

// Order states (NIP-CO §Order State).
const (
	CashOrderOpen    = "open"
	CashOrderPaid    = "paid"
	CashOrderMinted  = "minted"
	CashOrderExpired = "expired"
	CashOrderClosed  = "closed"
)

// CashOrder is one placed order. No column holds a buyer secret key, a cash secret
// or a connection secret. TermsJSON is the kind's terms and the bill terms frozen
// at placement; ReservedMillis its reservation against the open-order ceiling,
// 0 once it is minted, expires or is closed.
type CashOrder struct {
	ID              uint    `gorm:"primaryKey"`
	RequestID       string  `gorm:"size:64;not null;uniqueIndex"`
	HubAppID        uint    `gorm:"not null;index:idx_cash_orders_hub_payment_state,priority:1"`
	OrderConnAppID  uint    `gorm:"not null"`
	OrderConnPubkey string  `gorm:"size:64;not null;index"`
	BuyerPubkey     string  `gorm:"size:64;not null"`
	RecipientType   string  `gorm:"size:16;not null"`
	RecipientValue  string  `gorm:"size:64;not null"`
	RecipientIA     *string `gorm:"size:64"`
	AmountMillis    int64   `gorm:"not null"`
	Payment         string  `gorm:"size:10;not null;index:idx_cash_orders_hub_payment_state,priority:2"`
	Notify          string  `gorm:"size:64;not null"`
	ArrivalRelay    string  `gorm:"type:text;not null"`
	TermsJSON       string  `gorm:"type:text;not null"`
	FeeMillis       int64   `gorm:"not null"`
	PayMillis       int64   `gorm:"not null"`
	Confirmations   *int
	PayBy           int64   `gorm:"not null;index"`
	Deadline        int64   `gorm:"not null"`
	PaymentHash     *string `gorm:"size:64;uniqueIndex"`
	Invoice         *string `gorm:"type:text"`
	Address         *string `gorm:"size:100;uniqueIndex"`
	State           string  `gorm:"size:8;not null;index:idx_cash_orders_hub_payment_state,priority:3"`
	ReservedMillis  int64   `gorm:"not null"`
	CreatedAt       int64   `gorm:"not null"`
	StateAt         int64   `gorm:"not null"`
}

// CashOrderAddress is an address handed to an order, recorded before it is given
// out and never given out again. OrderID is nil until an order binds it; Burnt
// marks one whose placement failed or whose order was deleted unpaid.
type CashOrderAddress struct {
	Address  string `gorm:"primaryKey;size:100"`
	HubAppID uint   `gorm:"not null;index"`
	OrderID  *uint  `gorm:"index"`
	IssuedAt int64  `gorm:"not null"`
	Burnt    bool   `gorm:"not null;default:false"`
}
