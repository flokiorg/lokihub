// Package circlemoney is a Circle Wallet's money model (NIP-CW §Own Funds and
// Caps): the four kinds of payment, the send and receive caps per budget
// period, the circle's holdings and outbound ceilings, and the routing-fee
// reserve. Every check runs inside the caller's transaction, under the locks
// the caller took (LockHub, LockApp), so a check and the row it admits commit
// together.
package circlemoney

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/ohstr/nmilat/nip47"
	"github.com/ohstr/nmilat/nipcw"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/logger"
)

// The four kinds of payment, by where the other side is.
const (
	// KindSameCircle is another wallet issued by the same Hub.
	KindSameCircle = 1
	// KindOtherCircle is a Circle Wallet of another Hub on the node.
	KindOtherCircle = 2
	// KindNode is any other connection on the node: the host's own wallet, a
	// Cash Hub or a bill, any other application.
	KindNode = 3
	// KindLightning is anyone off the node.
	KindLightning = 4
)

// Defaults and limits lokihub applies.
const (
	// DefaultFeeReservePpm and DefaultFeeReserveMinMloki are the routing-fee
	// reserve a Hub gets unless its host sets another.
	DefaultFeeReservePpm      = constants.CIRCLE_DEFAULT_FEE_RESERVE_PPM
	DefaultFeeReserveMinMloki = constants.CIRCLE_DEFAULT_FEE_RESERVE_MIN_MLOKI
	// DefaultOutboundPeriod is the outbound ceiling's period unless set.
	DefaultOutboundPeriod = constants.CIRCLE_DEFAULT_OUTBOUND_PERIOD
	// MaxOpenInvoices is how many open invoices one wallet may hold at once.
	MaxOpenInvoices = 100
	// MaxPendingPayments is how many payments one wallet may have pending.
	MaxPendingPayments = 32
	// MaxInflightExternal is how many external payments (kinds 3 and 4) one
	// wallet may have in flight at once.
	MaxInflightExternal = 8
	// SameCircleTransfersPerMinute bounds a wallet's same-circle transfers.
	SameCircleTransfersPerMinute = 60
	// InvoicesPerMinute bounds the invoices one wallet may issue.
	InvoicesPerMinute = 60
)

// Refusal is a circle money check's refusal: its NIP-47 code and message.
type Refusal struct {
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

func refuse(code, format string, args ...any) error {
	return &Refusal{Code: code, Message: fmt.Sprintf(format, args...)}
}

// AsRefusal reports err's refusal, if it is one.
func AsRefusal(err error) (*Refusal, bool) {
	var r *Refusal
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}

// IsCircleWallet reports whether app is a Circle Wallet.
func IsCircleWallet(app *db.App) bool {
	return app != nil && app.Kind == db.AppKindCircleWallet && app.ParentAppID != nil
}

// Kind classifies a payment between wallet, a Circle Wallet, and other, the
// app on the other side (nil when it is the host's own wallet or no app), from
// wallet's side. sameNode is false for a payment that crosses Lightning.
func Kind(wallet, other *db.App, sameNode bool) int {
	if !sameNode {
		return KindLightning
	}
	if IsCircleWallet(wallet) && IsCircleWallet(other) {
		if *wallet.ParentAppID == *other.ParentAppID {
			return KindSameCircle
		}
		return KindOtherCircle
	}
	return KindNode
}

// External reports whether a kind is external traffic, which counts on the caps.
func External(kind int) bool { return kind == KindNode || kind == KindLightning }

// FeeReserve is a Hub's routing-fee reserve rule.
func FeeReserve(cfg *db.CircleHubConfig) nip47.FeeReserve {
	return nip47.FeeReserve{Ppm: int64(cfg.FeeReservePpm), MinMillis: cfg.FeeReserveMinMloki}
}

// HubConfig loads a Hub's config inside tx.
func HubConfig(tx *gorm.DB, hubID uint) (*db.CircleHubConfig, error) {
	var cfg db.CircleHubConfig
	if err := tx.Where("app_id = ?", hubID).First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("circle hub config for app %d: %w", hubID, err)
	}
	return &cfg, nil
}

// Caps are a wallet's send cap (max_amount) and receive cap (max_receive) and
// its renewal, in mloki. NoSendCap is a wallet without a send cap at all (one
// not made by a join, whose permission says 0); otherwise MaxAmount is the cap,
// 0 included.
type Caps struct {
	MaxAmount  int64
	NoSendCap  bool
	MaxReceive int64
	Renewal    string
}

// WalletCaps reads a wallet's caps: max_amount from the wallet's exact cap
// (CircleMaxAmountMloki), else from its pay_invoice permission (whole loki, 0
// meaning none); max_receive from the wallet.
func WalletCaps(tx *gorm.DB, wallet *db.App) (Caps, error) {
	var perm db.AppPermission
	err := tx.Where("app_id = ? AND scope = ?", wallet.ID, constants.PAY_INVOICE_SCOPE).Limit(1).Find(&perm).Error
	if err != nil {
		return Caps{}, err
	}
	caps := Caps{MaxAmount: int64(perm.MaxAmountLoki) * 1000, Renewal: perm.BudgetRenewal}
	if wallet.CircleMaxAmountMloki != nil {
		caps.MaxAmount = max(*wallet.CircleMaxAmountMloki, 0)
	} else {
		caps.NoSendCap = caps.MaxAmount <= 0
	}
	if caps.Renewal == "" {
		caps.Renewal = constants.BUDGET_RENEWAL_NEVER
	}
	if wallet.CircleMaxReceiveMloki != nil {
		caps.MaxReceive = *wallet.CircleMaxReceiveMloki
	}
	return caps, nil
}

// Anchor is when a wallet's budget periods count from.
func Anchor(wallet *db.App) time.Time {
	if wallet.CircleBudgetAnchorAt != nil {
		return time.Unix(*wallet.CircleBudgetAnchorAt, 0)
	}
	return wallet.CreatedAt
}

// Period is the budget period now falls in, [start, next); next is zero for
// never.
func Period(anchor time.Time, renewal string, now time.Time) (start, next time.Time, err error) {
	return nipcw.BudgetPeriod(anchor, renewal, now)
}

// HubPeriodStart is the start of the Hub's own outbound period now falls in,
// counted from the Hub's creation.
func HubPeriodStart(hub *db.App, cfg *db.CircleHubConfig, now time.Time) (int64, error) {
	period := cfg.OutboundPeriod
	if period == "" {
		period = DefaultOutboundPeriod
	}
	start, _, err := nipcw.BudgetPeriod(hub.CreatedAt, period, now)
	if err != nil {
		return 0, err
	}
	return start.Unix(), nil
}

// SendUsage is a wallet's external sends in the period starting at
// periodStart: every outgoing row counted on its send cap, pending at the most
// it may cost and settled at what it cost.
func SendUsage(tx *gorm.DB, walletID uint, periodStart int64) int64 {
	var sum int64
	if err := tx.Model(&db.Transaction{}).
		Select("COALESCE(SUM(amount_mloki + fee_mloki + fee_reserve_mloki + fee_skim_mloki), 0)").
		Where("app_id = ? AND type = ? AND state IN ? AND counts_send = ? AND period_start = ?",
			walletID, constants.TRANSACTION_TYPE_OUTGOING,
			[]string{constants.TRANSACTION_STATE_PENDING, constants.TRANSACTION_STATE_SETTLED}, true, periodStart).
		Scan(&sum).Error; err != nil {
		return usageUnknown(err, "send usage", walletID)
	}
	return sum
}

// Receipts is a wallet's settled external receipts (kinds 3 and 4) in the
// period starting at periodStart.
func Receipts(tx *gorm.DB, walletID uint, periodStart int64) int64 {
	var sum int64
	if err := tx.Model(&db.Transaction{}).
		Select("COALESCE(SUM(amount_mloki), 0)").
		Where("app_id = ? AND type = ? AND state = ? AND payment_kind IN ? AND period_start = ?",
			walletID, constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_SETTLED,
			[]int{KindNode, KindLightning}, periodStart).
		Scan(&sum).Error; err != nil {
		return usageUnknown(err, "receipts", walletID)
	}
	return sum
}

// OpenInvoices is the sum and count of a wallet's open invoices: unpaid ones
// not yet expired, and held payments the Hub accepted, whoever pays them.
// Expiry is compared in Go: the sqlite driver stores times in a form SQL date
// functions cannot read.
func OpenInvoices(tx *gorm.DB, walletIDs []uint, now time.Time) (sum int64, count int) {
	if len(walletIDs) == 0 {
		return 0, 0
	}
	var rows []db.Transaction
	if err := tx.Model(&db.Transaction{}).Select("amount_mloki", "expires_at", "state").
		Where("app_id IN ? AND type = ? AND state IN ?", walletIDs, constants.TRANSACTION_TYPE_INCOMING,
			[]string{constants.TRANSACTION_STATE_PENDING, constants.TRANSACTION_STATE_ACCEPTED}).
		Find(&rows).Error; err != nil {
		return usageUnknown(err, "open invoices", walletIDs[0]), math.MaxInt32
	}
	for _, row := range rows {
		if row.State == constants.TRANSACTION_STATE_ACCEPTED || row.ExpiresAt == nil || row.ExpiresAt.After(now) {
			sum = satAdd(sum, clampInt64(row.AmountMloki))
			count++
		}
	}
	return sum, count
}

// ReceiveUsage is what counts against a wallet's receive cap this period: its
// external receipts plus its open invoices.
func ReceiveUsage(tx *gorm.DB, wallet *db.App, caps Caps, now time.Time) (used int64, err error) {
	start, _, err := Period(Anchor(wallet), caps.Renewal, now)
	if err != nil {
		return 0, err
	}
	open, _ := OpenInvoices(tx, []uint{wallet.ID}, now)
	return satAdd(Receipts(tx, wallet.ID, start.Unix()), open), nil
}

// ReceiveRoom is max_receive less ReceiveUsage, at least 0.
func ReceiveRoom(tx *gorm.DB, wallet *db.App, now time.Time) (int64, error) {
	caps, err := WalletCaps(tx, wallet)
	if err != nil {
		return 0, err
	}
	used, err := ReceiveUsage(tx, wallet, caps, now)
	if err != nil {
		return 0, err
	}
	return max(caps.MaxReceive-used, 0), nil
}

// HoldingWalletIDs are the wallets of a Hub that count toward its holdings:
// the Active ones at now (NIP-CW §Lifecycle and Deletion).
func HoldingWalletIDs(tx *gorm.DB, hubID uint, now time.Time) ([]uint, error) {
	var wallets []db.App
	if err := tx.Model(&db.App{}).Select("id", "circle_state", "expires_at").
		Where("parent_app_id = ? AND kind = ?", hubID, db.AppKindCircleWallet).
		Find(&wallets).Error; err != nil {
		return nil, err
	}
	ids := make([]uint, 0, len(wallets))
	for i := range wallets {
		if circlelife.IsActive(&wallets[i], now) {
			ids = append(ids, wallets[i].ID)
		}
	}
	return ids, nil
}

// Holdings are the balances of a Hub's Active wallets plus every open invoice
// they issued.
func Holdings(tx *gorm.DB, hubID uint, now time.Time) (int64, error) {
	ids, err := HoldingWalletIDs(tx, hubID, now)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	balances, err := queries.GetIsolatedBalancesByAppIDs(tx, ids)
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, balance := range balances {
		sum = satAdd(sum, balance)
	}
	open, _ := OpenInvoices(tx, ids, now)
	return satAdd(sum, open), nil
}

// Available is what the circle can still take in: its holdings ceiling less
// its holdings, at least 0.
func Available(tx *gorm.DB, hubID uint, cfg *db.CircleHubConfig, now time.Time) (int64, error) {
	holdings, err := Holdings(tx, hubID, now)
	if err != nil {
		return 0, err
	}
	return max(cfg.HoldingsCeilingMloki-holdings, 0), nil
}

// CheckHoldings refuses amount when it would take the Hub's holdings past its
// ceiling (INSUFFICIENT_CAPACITY).
func CheckHoldings(tx *gorm.DB, hubID uint, cfg *db.CircleHubConfig, amount uint64, now time.Time) error {
	holdings, err := Holdings(tx, hubID, now)
	if err != nil {
		return err
	}
	if amount > math.MaxInt64 || satAdd(holdings, int64(amount)) > cfg.HoldingsCeilingMloki {
		return refuse(nipcw.ErrInsufficientCapacity, "the circle's holdings would pass its holdings ceiling")
	}
	warnNearCeiling("holdings", hubID, satAdd(holdings, int64(amount)), cfg.HoldingsCeilingMloki)
	return nil
}

// warnNearCeiling logs a WARN once a ceiling is 90 % used (the operator alert of
// lokihub's plan; there is no metrics endpoint).
func warnNearCeiling(ceiling string, hubID uint, used, limit int64) {
	if limit > 0 && used >= limit/10*9 {
		logger.Logger.Warn().Str("ceiling", ceiling).Uint("hub_app_id", hubID).Int64("used", used).Int64("limit", limit).
			Msg("A circle is at 90% or more of a ceiling")
	}
}

// OutboundUsage is what a Hub's wallets together sent as external traffic in
// the Hub period starting at hubPeriodStart.
func OutboundUsage(tx *gorm.DB, hubID uint, hubPeriodStart int64) int64 {
	var sum int64
	if err := tx.Table("transactions").
		Select("COALESCE(SUM(transactions.amount_mloki + transactions.fee_mloki + transactions.fee_reserve_mloki + transactions.fee_skim_mloki), 0)").
		Joins("JOIN apps ON apps.id = transactions.app_id").
		Where("apps.parent_app_id = ? AND apps.kind = ?", hubID, db.AppKindCircleWallet).
		Where("transactions.type = ? AND transactions.state IN ? AND transactions.outbound_period_start = ?",
			constants.TRANSACTION_TYPE_OUTGOING,
			[]string{constants.TRANSACTION_STATE_PENDING, constants.TRANSACTION_STATE_SETTLED}, hubPeriodStart).
		Scan(&sum).Error; err != nil {
		return usageUnknown(err, "outbound usage", hubID)
	}
	return sum
}

// usageUnknown is the usage a sum reads when the database could not answer: the
// most there can be, so every cap and ceiling it feeds refuses rather than
// passes.
func usageUnknown(err error, what string, appID uint) int64 {
	logger.Logger.Error().Err(err).Uint("app_id", appID).Str("sum", what).
		Msg("Could not read circle usage; treating it as full")
	return math.MaxInt64
}

func satAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func clampInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}
