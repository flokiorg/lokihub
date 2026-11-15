package config

// WithdrawEnv are the on-chain withdrawal tunables (NIP-CASH §Redeeming On-chain):
// advanced values for tests and tuning, never shown in the UI. The node switch
// that turns withdrawals on is the user_configs key CashWithdrawEnabledKey, set
// by the operator. A zero value (an AppConfig built as a literal, as tests do)
// reads as the default.
type WithdrawEnv struct {
	// CashWithdrawSentBySecs: a quote's sent_by is now + this, or send_before.
	CashWithdrawSentBySecs int `envconfig:"CASH_WITHDRAW_SENT_BY_SECS" default:"3600"`
	// CashWithdrawMinLeadSecs: the earliest the Hub promises to broadcast by; a
	// send_before before now + this is too_slow.
	CashWithdrawMinLeadSecs int `envconfig:"CASH_WITHDRAW_MIN_LEAD_SECS" default:"600"`
	// CashWithdrawQuoteBlocks: a quote holds until tip + this.
	CashWithdrawQuoteBlocks int `envconfig:"CASH_WITHDRAW_QUOTE_BLOCKS" default:"2"`
	// CashWithdrawFeeTargetConf is the confirmation target of the fee estimate.
	CashWithdrawFeeTargetConf int `envconfig:"CASH_WITHDRAW_FEE_TARGET_CONF" default:"6"`
	// CashWithdrawBumpAfterBlocks: a version unconfirmed this many blocks after
	// its first broadcast is bumped.
	CashWithdrawBumpAfterBlocks int `envconfig:"CASH_WITHDRAW_BUMP_AFTER_BLOCKS" default:"6"`
	// CashWithdrawRebroadcastSecs: an unconfirmed version is published again this
	// often.
	CashWithdrawRebroadcastSecs int `envconfig:"CASH_WITHDRAW_REBROADCAST_SECS" default:"1800"`
	// CashWithdrawBatchWindowSecs: queued payouts wait at most this long for a
	// batch.
	CashWithdrawBatchWindowSecs int `envconfig:"CASH_WITHDRAW_BATCH_WINDOW_SECS" default:"600"`
	// CashWithdrawMaxOutputs bounds the payout outputs of one version.
	CashWithdrawMaxOutputs int `envconfig:"CASH_WITHDRAW_MAX_OUTPUTS" default:"50"`
	// CashWithdrawUnconfirmedPpm bounds the unconfirmed payouts, per million of
	// the confirmed on-chain balance.
	CashWithdrawUnconfirmedPpm int `envconfig:"CASH_WITHDRAW_UNCONFIRMED_PPM" default:"100000"`
	// CashWithdrawFloorReserveMult: the coins never set aside are this many
	// times the node's required reserve.
	CashWithdrawFloorReserveMult int `envconfig:"CASH_WITHDRAW_FLOOR_RESERVE_MULT" default:"2"`
	// CashWithdrawLeaseSecs is how long a set-aside coin is leased at the node.
	CashWithdrawLeaseSecs int `envconfig:"CASH_WITHDRAW_LEASE_SECS" default:"2592000"`
}

// CashWithdrawEnabledKey is the user_configs key of the node switch: "true"
// offers on-chain withdrawals on every Cash Hub whose own terms enable them.
// Absent or anything else is off.
const CashWithdrawEnabledKey = "CashWithdrawEnabled"

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

// The tunables with their defaults applied.
func (w WithdrawEnv) SentBySecs() int64    { return int64(orDefault(w.CashWithdrawSentBySecs, 3600)) }
func (w WithdrawEnv) MinLeadSecs() int64   { return int64(orDefault(w.CashWithdrawMinLeadSecs, 600)) }
func (w WithdrawEnv) QuoteBlocks() int64   { return int64(orDefault(w.CashWithdrawQuoteBlocks, 2)) }
func (w WithdrawEnv) FeeTargetConf() int   { return orDefault(w.CashWithdrawFeeTargetConf, 6) }
func (w WithdrawEnv) BumpAfterBlocks() int { return orDefault(w.CashWithdrawBumpAfterBlocks, 6) }
func (w WithdrawEnv) RebroadcastSecs() int64 {
	return int64(orDefault(w.CashWithdrawRebroadcastSecs, 1800))
}
func (w WithdrawEnv) BatchWindowSecs() int64 {
	return int64(orDefault(w.CashWithdrawBatchWindowSecs, 600))
}
func (w WithdrawEnv) MaxOutputs() int { return orDefault(w.CashWithdrawMaxOutputs, 50) }
func (w WithdrawEnv) UnconfirmedPpm() int64 {
	return int64(orDefault(w.CashWithdrawUnconfirmedPpm, 100000))
}
func (w WithdrawEnv) FloorReserveMult() int64 {
	return int64(orDefault(w.CashWithdrawFloorReserveMult, 2))
}
func (w WithdrawEnv) LeaseSecs() int64 { return int64(orDefault(w.CashWithdrawLeaseSecs, 2592000)) }
