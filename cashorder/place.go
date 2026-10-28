package cashorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/ohstr/nmilat/nipCO"
	"github.com/ohstr/nmilat/nipcash"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/logger"
)

// IATrustChecker reports whether an Identity Authority is trusted now.
type IATrustChecker interface {
	IsTrusted(pubkey string) (bool, error)
}

// PlaceDeps is what placing an order needs beyond Deps.
type PlaceDeps struct {
	Deps
	IA IATrustChecker
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

func (d PlaceDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// PlaceInput is one cash_order item: the Order connection it targets, its params,
// the buyer key that signed its item proof, its deadline and the relay it
// arrived on.
type PlaceInput struct {
	Conn         *db.App
	Params       json.RawMessage
	Buyer        string
	Deadline     int64
	ArrivalRelay string
}

// ErrNotReceivable: the node could not be asked whether it can receive.
var ErrNotReceivable = errors.New("could not read the node's inbound capacity")

// InvoicePayBy is when a Lightning order's invoice expires: its BOLT11
// timestamp plus its expiry. That instant is the order's pay_by exactly
// (NIP-CO §Lightning), which clients check against the invoice.
func InvoicePayBy(invoice string) (int64, error) {
	decoded, err := decodepay.Decode(invoice)
	if err != nil {
		return 0, fmt.Errorf("decode the order invoice: %w", err)
	}
	if decoded.CreatedAt <= 0 || decoded.Expiry <= 0 {
		return 0, fmt.Errorf("the order invoice has no expiry")
	}
	return decoded.CreatedAt + decoded.Expiry, nil
}

// orderTerms is what an order keeps of the terms it was placed under: its
// kind's terms and the bill terms.
type orderTerms struct {
	Kind nipCO.KindTerms `json:"kind"`
	Bill nipCO.BillTerms `json:"bill"`
}

// Place runs NIP-CO §Processing Algorithm steps 2 to 8 for an item whose
// request_id is already marked used (step 1). A refusal is an *nipCO.OrderError;
// cashrequest.ErrDeadlinePassed is BAD_REQUEST; any other error is the Hub's.
// Nothing moves funds: no ledger row is written.
func Place(ctx context.Context, deps PlaceDeps, in PlaceInput) (*nipCO.Order, error) {
	req, perr := nipCO.ParseCashOrderRequest(in.Params)
	if req == nil {
		return nil, perr
	}
	conn, err := ConnectionOf(deps.DB, in.Conn.ID)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, fmt.Errorf("app %d is not an order connection", in.Conn.ID)
	}
	settings, _, err := GetSettings(deps.DB, conn.HubAppID)
	if err != nil {
		return nil, err
	}

	// The node's inbound capacity is read once, outside any transaction (an RPC).
	inbound := int64(-1)
	if req.Payment == nipCO.PaymentLightning {
		inbound = inboundCapacity(ctx, deps)
	}
	input, err := placementInput(deps, deps.DB, conn, settings, req, inbound)
	if err != nil {
		return nil, err
	}
	if err := nipCO.CheckPlacement(req, input); err != nil {
		return nil, err
	}
	kind := settings.Terms.Kind(req.Payment)
	fee, pay, err := nipCO.OrderFee(req.Payment, *kind, req.AmountMillis)
	if err != nil || pay > math.MaxInt64 {
		return nil, &nipCO.OrderError{Code: nipcash.ErrCodeRestricted, Reason: "the amount is too large", Step: 4}
	}
	now := deps.now()
	window := int64(kind.Window) //nolint:gosec // validated at most a week
	payBy := now.Unix() + window

	order := &db.CashOrder{
		RequestID: req.RequestID, HubAppID: conn.HubAppID, OrderConnAppID: conn.AppID, OrderConnPubkey: *in.Conn.WalletPubkey,
		BuyerPubkey: in.Buyer, RecipientType: req.Recipient.IdentityType, RecipientValue: req.Recipient.IdentityValue,
		AmountMillis: int64(req.AmountMillis), Payment: req.Payment, Notify: req.Notify, ArrivalRelay: in.ArrivalRelay, //nolint:gosec // below pay
		FeeMillis: int64(fee), PayMillis: int64(pay), PayBy: payBy, Deadline: in.Deadline, //nolint:gosec // below 2^63
		State: db.CashOrderOpen, ReservedMillis: int64(req.AmountMillis), CreatedAt: now.Unix(), StateAt: now.Unix(), //nolint:gosec // below pay
	}
	if req.Recipient.IAPubkey != "" {
		ia := req.Recipient.IAPubkey
		order.RecipientIA = &ia
	}
	terms, err := json.Marshal(orderTerms{Kind: *kind, Bill: settings.Terms.Bill})
	if err != nil {
		return nil, err
	}
	order.TermsJSON = string(terms)

	// Step 6: the invoice or the address, outside any transaction (an RPC).
	var undo func()
	switch req.Payment {
	case nipCO.PaymentLightning:
		inv, err := deps.LN.MakeInvoice(ctx, int64(pay), "cash order", "", window, nil, nil, nil, nil, nil) //nolint:gosec // below 2^63
		if err != nil {
			return nil, fmt.Errorf("create the order invoice: %w", err)
		}
		hash, invoice := inv.PaymentHash, inv.Invoice
		order.PaymentHash, order.Invoice = &hash, &invoice
		undo = func() {
			if err := deps.LN.CancelHoldInvoice(context.Background(), hash); err != nil {
				logger.Logger.Error().Err(err).Str("payment_hash", hash).
					Msg("Failed to cancel the invoice of an order that was not placed; a payment of it is kept by the Hub")
			}
		}
		// The node stamps the invoice with its own clock, after an RPC or two, so
		// pay_by is read back from the invoice rather than computed here.
		order.PayBy, err = InvoicePayBy(invoice)
		if err != nil {
			undo()
			return nil, err
		}
	case nipCO.PaymentOnchain:
		address, err := NewOrderAddress(ctx, deps.Deps, conn.HubAppID, now)
		if err != nil {
			return nil, err
		}
		n, err := nipCO.ConfirmationsFor(kind.Confirmations, pay)
		if err != nil {
			BurnAddress(deps.DB, address)
			return nil, err
		}
		c := int(n)
		order.Address, order.Confirmations = &address, &c
		undo = func() { BurnAddress(deps.DB, address) }
	}

	// Step 7: one commit, under the Hub's order lock, re-checking the ceiling and
	// the deadline.
	err = deps.DB.Transaction(func(tx *gorm.DB) error {
		// The Hub's own lock first, as a Hub delete and a mint take it: an order
		// never commits under a Hub whose delete check already passed.
		if err := db.LockApp(tx, conn.HubAppID); err != nil {
			return err
		}
		if err := db.LockNamespace(tx, db.LockNsOrder, int32(conn.HubAppID)); err != nil { //nolint:gosec // app ids are small
			return err
		}
		var hubs int64
		if err := tx.Model(&db.App{}).Where("id = ?", conn.HubAppID).Count(&hubs).Error; err != nil {
			return err
		}
		if hubs == 0 {
			return &nipCO.OrderError{Code: nipcash.ErrCodeRestricted, Reason: "this Cash Hub no longer sells", Step: 7}
		}
		input, err := placementInput(deps, tx, conn, settings, req, inbound)
		if err != nil {
			return err
		}
		if err := nipCO.CheckPlacement(req, input); err != nil {
			return err
		}
		if err := tx.Create(order).Error; err != nil {
			return err
		}
		if order.Address != nil {
			res := tx.Model(&db.CashOrderAddress{}).Where("address = ? AND order_id IS NULL AND burnt = ?", *order.Address, false).
				Update("order_id", order.ID)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return fmt.Errorf("the order address was taken")
			}
		}
		orderID := order.ID
		record := &db.CashRequestRecord{
			RequestID: req.RequestID, Method: nipcash.MethodCashOrder, OrderID: &orderID, HubAppID: conn.HubAppID,
			TargetWalletPubkey: *in.Conn.WalletPubkey, TargetConnPubkey: in.Conn.AppPubkey,
			AuthorizerKind: "pubkey", Authorizer: in.Buyer, Deadline: in.Deadline, ResultMeta: "{}",
		}
		if err := cashrequest.New(deps.DB).CommitTx(tx, record, nil); err != nil {
			return err
		}
		// An order's record follows NIP-CO §Reading an Order, set by its retention.
		if err := tx.Model(record).Update("drop_after", nil).Error; err != nil {
			return err
		}
		if !nipcash.CommitAllowed(in.Deadline, deps.now().Unix()) {
			return cashrequest.ErrDeadlinePassed
		}
		return nil
	})
	if err != nil {
		undo()
		return nil, err
	}
	logger.Logger.Info().Uint("order_id", order.ID).Uint("hub_app_id", order.HubAppID).Str("payment", order.Payment).
		Int64("amount_millis", order.AmountMillis).Msg("Placed a cash order")
	view := View(order, nil)
	return &view, nil
}

// placementInput is what CheckPlacement needs, read through q (inside the
// commit, under the order lock, for the re-check). inbound is the node's
// inbound capacity in millis, negative when it could not be read.
func placementInput(deps PlaceDeps, q *gorm.DB, conn *db.CashOrderConnection, s Settings, req *nipCO.CashOrderRequest, inbound int64) (nipCO.PlacementInput, error) {
	in := nipCO.PlacementInput{Terms: s.Terms, Retired: conn.RetiredAt != nil, IATrusted: true}
	if req.Recipient.IdentityType == "connection_key" && req.Recipient.IAPubkey != "" {
		trusted := false
		if deps.IA != nil {
			ok, err := deps.IA.IsTrusted(req.Recipient.IAPubkey)
			if err != nil {
				return in, err
			}
			trusted = ok
		}
		in.IATrusted = trusted
	}
	var open struct {
		Sum   int64
		Count int64
	}
	if err := q.Model(&db.CashOrder{}).Select("COALESCE(SUM(reserved_millis), 0) AS sum, COUNT(*) AS count").
		Where("hub_app_id = ? AND payment = ? AND reserved_millis > 0", conn.HubAppID, req.Payment).Scan(&open).Error; err != nil {
		return in, err
	}
	in.OpenMillis = uint64(max(open.Sum, 0)) //nolint:gosec // non-negative
	switch req.Payment {
	case nipCO.PaymentLightning:
		in.OpenCeiling = s.OpenCeilingLnMillis
		if k := s.Terms.Kind(req.Payment); k != nil && inbound >= 0 {
			_, pay, err := nipCO.OrderFee(nipCO.PaymentLightning, *k, req.AmountMillis)
			if err == nil && pay <= math.MaxInt64 {
				var pending int64
				if err := q.Model(&db.CashOrder{}).Select("COALESCE(SUM(pay_millis), 0)").
					Where("payment = ? AND state = ?", nipCO.PaymentLightning, db.CashOrderOpen).Scan(&pending).Error; err != nil {
					return in, err
				}
				in.LightningReceivable = inbound >= int64(pay)+pending //nolint:gosec // checked
			}
		}
	case nipCO.PaymentOnchain:
		in.OpenCeiling = s.OpenCeilingOnchainMillis
		in.OpenOnchainCount = uint64(max(open.Count, 0)) //nolint:gosec // non-negative
		in.OpenOnchainCeiling = uint64(max(s.OpenOnchainMax, 0))
	}
	return in, nil
}

// inboundCapacity is the inbound capacity of the node's active channels in
// millis (OQ-16), -1 when the node does not answer: a Lightning order is then
// one the Hub cannot expect to receive.
func inboundCapacity(ctx context.Context, deps PlaceDeps) int64 {
	channels, err := deps.LN.ListChannels(ctx)
	if err != nil {
		logger.Logger.Warn().Err(err).Msg("Could not list channels to place a Lightning order")
		return -1
	}
	var inbound int64
	for _, c := range channels {
		if c.Active && c.RemoteBalance > 0 {
			inbound += c.RemoteBalance
		}
	}
	return inbound
}

// View is an order as cash_order and cash_lookup answer it (NIP-CO §Response).
func View(o *db.CashOrder, bills []nipCO.OrderBill) nipCO.Order {
	v := nipCO.Order{
		State: nipCO.OrderState(o.State), Payment: o.Payment,
		AmountMillis: uint64(o.AmountMillis), FeeMillis: uint64(o.FeeMillis), PayMillis: uint64(o.PayMillis), //nolint:gosec // non-negative
		PayBy: o.PayBy, Bills: bills,
	}
	if o.Invoice != nil {
		v.Invoice = *o.Invoice
	}
	if o.Address != nil {
		v.Address = *o.Address
	}
	if o.Confirmations != nil {
		v.Confirmations = uint32(*o.Confirmations) //nolint:gosec // a tier's n
	}
	return v
}
