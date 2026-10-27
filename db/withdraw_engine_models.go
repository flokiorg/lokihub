package db

// The payout engine's tables (WP-24, NIP-CASH §Paying a Payout): batches, the
// versions of each (every transaction that may have been signed, recorded
// before any signature is asked for), which payout each version pays at which
// output, the node's coins as the engine sees them, and the amount set aside
// for each payout. Kept in their own file, as the withdrawal tables are.

// Batch states.
const (
	CashBatchOpen      = "open"
	CashBatchSigned    = "signed"
	CashBatchSettled   = "settled"
	CashBatchAbandoned = "abandoned"
)

// CashWithdrawBatch is a set of payouts every version of which conflicts with
// every other: a payout belongs to one batch for life.
type CashWithdrawBatch struct {
	ID        uint   `gorm:"primaryKey"`
	CreatedAt int64  `gorm:"not null"`
	State     string `gorm:"size:12;not null;index"`
}

// Version kinds and states.
const (
	CashVersionPayout       = "payout"
	CashVersionInvalidation = "invalidation"
	CashVersionChild        = "child"

	CashVersionAboutToSign = "about_to_sign"
	CashVersionSigned      = "signed"
	CashVersionBroadcast   = "broadcast"
	CashVersionConfirmed   = "confirmed"
	CashVersionFinal       = "final"
	CashVersionReplaced    = "replaced"
	CashVersionConflicted  = "conflicted"
	CashVersionInvalid     = "invalid"
)

// CashWithdrawVersion is one transaction of a batch. Inputs, outputs and the
// PSBT are written before any signature is asked for (the about-to-sign
// record); the raw transaction is stored before it is broadcast.
type CashWithdrawVersion struct {
	ID      uint   `gorm:"primaryKey"`
	BatchID uint   `gorm:"not null;uniqueIndex:idx_cash_withdraw_versions_batch_seq,priority:1"`
	Seq     int    `gorm:"not null;uniqueIndex:idx_cash_withdraw_versions_batch_seq,priority:2"`
	Kind    string `gorm:"size:12;not null"`
	State   string `gorm:"size:14;not null;index"`
	// Inputs is a JSON array of outpoints; Outputs a JSON array of
	// {script, value, withdrawal_id|null}.
	Inputs  string  `gorm:"type:text;not null"`
	Outputs string  `gorm:"type:text;not null"`
	Psbt    string  `gorm:"type:text;not null"`
	RawTx   *string `gorm:"type:text"`
	Txid    *string `gorm:"size:64;uniqueIndex"`
	Vsize   *int
	FeeSat  *int64
	// FeeRateSatKvb is the fee per 1000 vbytes.
	FeeRateSatKvb *int64
	ConfirmedOnce bool `gorm:"not null;default:false"`
	// ParentID is a child's parent version.
	ParentID         *uint
	CreatedAt        int64 `gorm:"not null"`
	SignedAt         *int64
	FirstBroadcastAt *int64
	LastBroadcastAt  *int64
	// BroadcastHeight is the tip at the first broadcast (bumps count from it).
	BroadcastHeight *int64
	ConfirmHeight   *int64
	ConfirmHash     *string `gorm:"size:64"`
	// FeeRefused marks a version the network refused for its fee: bump now.
	FeeRefused bool `gorm:"not null;default:false"`
}

// CashWithdrawVersionOutput is the output of a version paying a payout.
type CashWithdrawVersionOutput struct {
	VersionID    uint `gorm:"primaryKey;autoIncrement:false"`
	WithdrawalID uint `gorm:"primaryKey;autoIncrement:false;index"`
	Vout         int  `gorm:"not null"`
}

// Coin states.
const (
	CashCoinAvailable = "available"
	CashCoinSetAside  = "set_aside"
	CashCoinInVersion = "in_version"
	CashCoinSpent     = "spent"
	CashCoinVanished  = "vanished"
)

// CashWithdrawCoin is a node wallet coin as the engine sees it. This table, not
// the node's lease, is the source of truth for what is set aside.
type CashWithdrawCoin struct {
	Outpoint     string `gorm:"primaryKey;size:80"`
	ValueSat     int64  `gorm:"not null"`
	Script       string `gorm:"size:84;not null"`
	ConfHeight   int64
	State        string `gorm:"size:10;not null;index"`
	WithdrawalID *uint  `gorm:"index"`
	BatchID      *uint  `gorm:"index"`
	LeasedUntil  *int64
	SeenAt       int64
	VanishedAt   *int64
	// VanishedHeight is the tip when the coin left the wallet unspent by any
	// version of ours.
	VanishedHeight *int64
}

// CashWithdrawSetAside is the amount set aside for one payout, durable and
// without expiry: its payout plus three times its quoted network fee.
type CashWithdrawSetAside struct {
	WithdrawalID uint  `gorm:"primaryKey;autoIncrement:false"`
	AmountSat    int64 `gorm:"not null"`
	CreatedAt    int64 `gorm:"not null"`
}
