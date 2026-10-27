package db

// On-chain withdrawals (NIP-CASH §Redeeming On-chain): a Cash Hub's terms, one row
// per committed cash_withdraw (the payout), and the payout's archive facts. Kept in
// their own file so they rebase onto other schema changes without touching the
// main model list; the schema is created fresh, so there is no conversion.

// CashWithdrawTerms are one Cash Hub's on-chain withdrawal terms, as the operator
// sets them. Everything else in cash_status' withdraw object is derived.
type CashWithdrawTerms struct {
	HubAppID      uint  `gorm:"primaryKey;autoIncrement:false"`
	Hub           App   `gorm:"foreignKey:HubAppID;constraint:OnDelete:CASCADE"`
	Enabled       bool  `gorm:"not null;default:false"`
	FeeBaseMillis int64 `gorm:"not null;default:0"`
	FeePpm        int   `gorm:"not null;default:0"`
	// MinMillis and MaxMillis bound the slice amount; nil is no bound.
	MinMillis *int64
	MaxMillis *int64
	UpdatedAt int64
}

// TableName keeps the plan's name (one row per hub, so the singular "terms").
func (CashWithdrawTerms) TableName() string { return "cash_withdraw_terms" }

// Payout states (NIP-CASH §Payout States), as stored.
const (
	CashWithdrawQueued    = "queued"
	CashWithdrawBroadcast = "broadcast"
	CashWithdrawConfirmed = "confirmed"
	CashWithdrawFinal     = "final"
	CashWithdrawCancelled = "cancelled"
)

// Why a payout is being cancelled.
const (
	CashWithdrawCancelNoRecord   = "no_record"
	CashWithdrawCancelInvalidate = "invalidate"
	CashWithdrawCancelRequest    = "request"
)

// CashWithdrawal is one committed cash_withdraw: the payout the Hub owes. It holds
// no cash secret and no re-mint secret (only remint_commitment); address and
// notify are never logged.
type CashWithdrawal struct {
	ID        uint   `gorm:"primaryKey"`
	RequestID string `gorm:"size:64;not null;uniqueIndex"`
	RecordID  uint   `gorm:"not null"` // its cash_request_records row
	HubAppID  uint   `gorm:"not null;index:idx_cash_withdrawals_hub_state,priority:1"`
	// BillAppID has no foreign key: the payout outlives the bill.
	BillAppID        uint   `gorm:"not null"`
	BillWalletPubkey string `gorm:"size:64;not null;index"`
	// ClaimID is the source slice, live or archived.
	ClaimID       uint   `gorm:"not null"`
	IdentityType  string `gorm:"size:16;not null"`
	IdentityValue string `gorm:"size:64;not null"`
	IAPubkey      string `gorm:"size:64"`
	// RemintCommitment is a cash-mode slice's re-mint target; DeliveryPubkey the
	// claim signer a re-minted bill is nested-encrypted to (nil for cash-mode).
	RemintCommitment *string `gorm:"size:64"`
	DeliveryPubkey   *string `gorm:"size:64"`
	// The slice's terms, for the re-mint.
	SliceExpiresAt      *int64
	Ordered             bool  `gorm:"not null;default:false"`
	RedeemFeeBaseMillis int64 `gorm:"not null;default:0"`
	RedeemFeePpm        int   `gorm:"not null;default:0"`
	MinTransferMillis   int64 `gorm:"not null;default:0"`
	// UnfinalPaymentIDs are the order payments the slice was made from (JSON),
	// copied to a re-minted bill.
	UnfinalPaymentIDs *string `gorm:"type:text"`

	QuoteEvent     string `gorm:"type:text;not null"`
	QuoteID        string `gorm:"size:64;not null"`
	Address        string `gorm:"size:90;not null"`
	WitnessVersion int16  `gorm:"not null"`
	OutputScript   string `gorm:"size:84;not null"` // hex

	AmountMillis     int64 `gorm:"not null"`
	NetworkFeeMillis int64 `gorm:"not null"`
	HubFeeMillis     int64 `gorm:"not null"`
	FeeMillis        int64 `gorm:"not null"`
	PayoutMillis     int64 `gorm:"not null"`
	SentBy           int64 `gorm:"not null;index:idx_cash_withdrawals_state_sent_by,priority:2"`
	HoldsUntil       int64 `gorm:"not null"`
	FinalDepth       int   `gorm:"not null"`

	Notify       string `gorm:"size:64;not null"`
	ArrivalRelay string `gorm:"type:text;not null"`
	ReceiptEvent string `gorm:"type:text;not null"`

	State string `gorm:"size:10;not null;index:idx_cash_withdrawals_hub_state,priority:2;index:idx_cash_withdrawals_state_sent_by,priority:1"`
	// Signed is set once, at the first about-to-sign record, and never reset;
	// BatchID with it, and never changed after (a payout belongs to one batch).
	Signed  bool  `gorm:"not null;default:false"`
	BatchID *uint `gorm:"index"`
	// Txid and Vout are the version the payout reports: the confirmed one, else
	// the last broadcast. Depth is that output's confirmations.
	Txid         *string `gorm:"size:64"`
	Vout         *int
	Depth        int  `gorm:"not null;default:0"`
	FinalReached bool `gorm:"not null;default:false"`
	// HeldMillis is the amount held for this payout: amount_millis from the
	// commit, 0 once final or re-minted.
	HeldMillis int64 `gorm:"not null"`

	CancelRequestedAt  *int64
	CancelReason       *string `gorm:"size:16"`
	FinalAt            *int64
	CancelledAt        *int64
	RemintAppID        *uint   `gorm:"index"`
	RemintWalletPubkey *string `gorm:"size:64"`

	NoticeDueAt *int64 `gorm:"index"`
	NoticesSent int16  `gorm:"not null;default:0"`
	CommittedAt int64  `gorm:"not null"`
	StateAt     int64  `gorm:"not null"`
}

// CashWithdrawEvent is one archive fact of a payout (NIP-CASH §Archival on
// Deletion): a state change with, once broadcast, the transaction id, the
// payout's output and its share of the miner fee. It outlives the payout row and
// holds no address.
type CashWithdrawEvent struct {
	ID             uint    `gorm:"primaryKey"`
	WithdrawalID   uint    `gorm:"not null;index"`
	At             int64   `gorm:"not null"`
	State          string  `gorm:"size:10;not null"`
	Txid           *string `gorm:"size:64"`
	Vout           *int
	Depth          *int
	MinerFeeMillis *int64
}
