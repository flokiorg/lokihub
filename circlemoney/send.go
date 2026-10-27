package circlemoney

import (
	"math"
	"math/bits"
	"time"

	"github.com/ohstr/nmilat/nipcw"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
)

// SendQuote is what a Circle Wallet's payment holds back and how it counts,
// stamped on its outgoing row.
type SendQuote struct {
	Kind int
	// FeeSkim is the fees_ppm fee, FeeReserve the routing-fee reserve; both 0
	// except on Lightning (kind 4).
	FeeSkim    uint64
	FeeReserve uint64
	// FullCost is the most the payment may cost: amount, fee and reserve.
	FullCost uint64
	// CountsSend and PeriodStart: counted on the wallet's send cap in that
	// period. OutboundPeriodStart: counted on the circle's outbound ceiling in
	// that Hub period (0: not counted).
	CountsSend          bool
	PeriodStart         int64
	OutboundPeriodStart int64
}

// SendInput is a payment a Circle Wallet is about to make.
type SendInput struct {
	Wallet *db.App
	// Payee is the app receiving on this node (nil for the host's own wallet,
	// or off the node); SameNode is false for a Lightning payment.
	Payee    *db.App
	SameNode bool
	Amount   uint64
	Now      time.Time
	// Active is whether the wallet is Active: only an Active wallet's payments
	// count on its send cap.
	Active bool
}

// CheckSend runs a Circle Wallet's send checks in their order (NIP-CW §Own
// Funds and Caps): the full cost against the balance (INSUFFICIENT_BALANCE),
// an Active wallet's send cap (QUOTA_EXCEEDED), the circle's outbound ceiling,
// for every state (QUOTA_EXCEEDED), the payments it has in flight
// (RATE_LIMITED), and the same-circle transfer rate (RATE_LIMITED). The caller
// holds the Hub's lock, then the wallet's.
func CheckSend(tx *gorm.DB, in SendInput, limiter *Limiter) (SendQuote, error) {
	wallet := in.Wallet
	cfg, err := HubConfig(tx, *wallet.ParentAppID)
	if err != nil {
		return SendQuote{}, err
	}
	q := SendQuote{Kind: Kind(wallet, in.Payee, in.SameNode)}
	if q.Kind == KindLightning {
		// The member's rate is the one fixed at its join (NIP-CW §Fees); a
		// wallet with none recorded pays the Hub's current one.
		rate := cfg.FeesPpm
		if wallet.CircleFeesPpm != nil {
			rate = *wallet.CircleFeesPpm
		}
		feesPpm := uint64(max(rate, 0))
		q.FeeSkim = nipcw.FeeSkim(in.Amount, feesPpm)
		q.FeeReserve = nipcw.RoutingFeeReserve(in.Amount, FeeReserve(cfg))
	}
	q.FullCost = addSat(addSat(in.Amount, q.FeeSkim), q.FeeReserve)

	balance := queries.GetIsolatedBalance(tx, wallet.ID)
	if q.FullCost > math.MaxInt64 || int64(q.FullCost) > balance {
		return SendQuote{}, refuse(constants.ERROR_INSUFFICIENT_BALANCE, "the payment's full cost exceeds the wallet's balance")
	}

	external := External(q.Kind)
	if external && in.Active {
		caps, err := WalletCaps(tx, wallet)
		if err != nil {
			return SendQuote{}, err
		}
		start, _, err := Period(Anchor(wallet), caps.Renewal, in.Now)
		if err != nil {
			return SendQuote{}, err
		}
		q.CountsSend, q.PeriodStart = true, start.Unix()
		used := SendUsage(tx, wallet.ID, q.PeriodStart)
		if caps.MaxAmount > 0 && satAdd(used, int64(q.FullCost)) > caps.MaxAmount {
			return SendQuote{}, refuse(constants.ERROR_QUOTA_EXCEEDED, "the payment would pass the wallet's send cap for this period")
		}
	}
	if external {
		var hub db.App
		if err := tx.Where("id = ?", *wallet.ParentAppID).First(&hub).Error; err != nil {
			return SendQuote{}, err
		}
		q.OutboundPeriodStart, err = HubPeriodStart(&hub, cfg, in.Now)
		if err != nil {
			return SendQuote{}, err
		}
		if cfg.OutboundCeilingMloki > 0 {
			outbound := satAdd(OutboundUsage(tx, hub.ID, q.OutboundPeriodStart), int64(q.FullCost))
			if outbound > cfg.OutboundCeilingMloki {
				return SendQuote{}, refuse(constants.ERROR_QUOTA_EXCEEDED, "the payment would pass the circle's outbound ceiling for this period")
			}
			warnNearCeiling("outbound", hub.ID, outbound, cfg.OutboundCeilingMloki)
		}
	}

	var pending []db.Transaction
	if err := tx.Model(&db.Transaction{}).Select("payment_kind").
		Where("app_id = ? AND type = ? AND state = ?", wallet.ID, constants.TRANSACTION_TYPE_OUTGOING, constants.TRANSACTION_STATE_PENDING).
		Find(&pending).Error; err != nil {
		return SendQuote{}, err
	}
	if len(pending) >= MaxPendingPayments {
		return SendQuote{}, refuse(constants.ERROR_RATE_LIMITED, "too many payments pending for this wallet")
	}
	if external {
		inflight := 0
		for _, p := range pending {
			if External(p.PaymentKind) {
				inflight++
			}
		}
		if inflight >= MaxInflightExternal {
			return SendQuote{}, refuse(constants.ERROR_RATE_LIMITED, "too many external payments in flight for this wallet")
		}
	}
	if q.Kind == KindSameCircle && limiter != nil && !limiter.Allow(wallet.ID, SameCircleTransfersPerMinute, in.Now) {
		return SendQuote{}, refuse(constants.ERROR_RATE_LIMITED, "too many same-circle transfers; retry later")
	}
	return q, nil
}

// CreditInput is a credit to a Circle Wallet that names none of its open
// invoices: a keysend.
type CreditInput struct {
	Wallet *db.App
	// Payer is the paying app on this node (nil for the host's own wallet, or
	// off the node); SameNode is false for one arriving over Lightning.
	Payer    *db.App
	SameNode bool
	Amount   uint64
	Now      time.Time
	// PayerActive is whether a paying Circle Wallet is Active.
	PayerActive bool
}

// CheckCredit decides whether a credit that names none of the wallet's open
// invoices may be credited: only to an Active wallet (PAYMENT_FAILED), within
// the wallet's receive room when it is
// external (QUOTA_EXCEEDED), and within the circle's holdings ceiling unless it
// only moves money between two Active wallets of the circle
// (INSUFFICIENT_CAPACITY). The caller holds the receiving Hub's lock.
func CheckCredit(tx *gorm.DB, in CreditInput) error {
	// Only an Active wallet receives; every payment to any other fails
	// (NIP-CW §Own Funds and Caps).
	if !circlelife.IsActive(in.Wallet, in.Now) {
		return refuse(constants.ERROR_PAYMENT_FAILED, "the receiving circle wallet is not active")
	}
	kind := Kind(in.Wallet, in.Payer, in.SameNode)
	if External(kind) {
		room, err := ReceiveRoom(tx, in.Wallet, in.Now)
		if err != nil {
			return err
		}
		if in.Amount > math.MaxInt64 || int64(in.Amount) > room {
			return refuse(constants.ERROR_QUOTA_EXCEEDED, "the payment is larger than the receiving wallet's receive room")
		}
	}
	if kind == KindSameCircle && in.PayerActive {
		return nil
	}
	cfg, err := HubConfig(tx, *in.Wallet.ParentAppID)
	if err != nil {
		return err
	}
	return CheckHoldings(tx, *in.Wallet.ParentAppID, cfg, in.Amount, in.Now)
}

func addSat(a, b uint64) uint64 {
	s, carry := bits.Add64(a, b, 0)
	if carry != 0 {
		return math.MaxUint64
	}
	return s
}
