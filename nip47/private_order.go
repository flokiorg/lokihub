package nip47

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/flokiorg/lokihub/hubclock"
	"time"

	"github.com/ohstr/nmilat/nipCO"
	"github.com/ohstr/nmilat/nipcash/transport"

	"github.com/flokiorg/lokihub/cashorder"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
)

// servePrivateOrder places a cash_order item on an Order connection (NIP-CO
// §Processing Algorithm) once its bill proof, request_id and item proof passed:
// the request_id is marked used now (step 1), then the order is placed.
func (svc *nip47Service) servePrivateOrder(ctx context.Context, lnClient lnclient.LNClient, item transport.Item,
	binding PrivateItemBinding, app *db.App, buyer string) (transport.Result, bool) {
	refuse := func(code, message string) (transport.Result, bool) {
		return transport.Result{ID: item.ID, ResultType: item.Method,
			Error: &transport.ResultError{Code: code, Message: message}}, true
	}
	var params struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(item.Params, &params); err != nil {
		return refuse(constants.ERROR_BAD_REQUEST, "cash_order params do not parse")
	}
	if err := cashrequest.New(svc.db).MarkUsed(params.RequestID, constants.NIP47MethodCashOrder); err != nil {
		if errors.Is(err, cashrequest.ErrRequestReused) {
			return refuse(constants.ERROR_REQUEST_REUSED, "this request_id was already used on this node")
		}
		logger.Logger.Error().Err(err).Str("item", item.ID).Msg("Failed to mark an order's request_id used")
		return refuse(constants.ERROR_INTERNAL, "the hub could not record this order, please retry")
	}

	deps := cashorder.PlaceDeps{Deps: cashorder.Deps{DB: svc.db, Keys: svc.keys, Apps: svc.appsService, LN: lnClient,
		Relays: svc.cfg.GetRelayUrls()}}
	if svc.identityAuthorityMgr != nil {
		deps.IA = svc.identityAuthorityMgr
	}
	order, err := cashorder.Place(ctx, deps, cashorder.PlaceInput{Conn: app, Params: item.Params, Buyer: buyer,
		Deadline: binding.NotAfter, ArrivalRelay: binding.ArrivalRelay})
	if err != nil {
		var refusal *nipCO.OrderError
		switch {
		case errors.As(err, &refusal):
			return refuse(refusal.Code, refusal.Reason)
		case errors.Is(err, cashrequest.ErrDeadlinePassed):
			return refuse(constants.ERROR_BAD_REQUEST, "the order reached its commit after its deadline")
		}
		logger.Logger.Error().Err(err).Str("item", item.ID).Uint("app_id", app.ID).Msg("Failed to place a cash order")
		return refuse(constants.ERROR_INTERNAL, "the hub could not place this order")
	}
	body, err := json.Marshal(order)
	if err != nil {
		return refuse(constants.ERROR_INTERNAL, "could not encode the order")
	}
	return transport.Result{ID: item.ID, ResultType: item.Method, Result: body}, true
}

// orderView is an order as it stands, its bills each with its token rebuilt,
// or its tombstone (NIP-CO §Reading an Order). An order past its pay_by whose
// window could not be ended is not answered: cashorder.ErrWindowNotEnded.
func (svc *nip47Service) orderView(rec *db.CashRequestRecord) (json.RawMessage, error) {
	if rec.OrderID == nil {
		return nil, errors.New("an order record without its order")
	}
	if end := svc.orderPayBy.Load(); end != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := (*end)(ctx, *rec.OrderID)
		cancel()
		if err != nil {
			return nil, err
		}
	}
	var order db.CashOrder
	if err := svc.db.First(&order, *rec.OrderID).Error; err != nil {
		return nil, err
	}
	// Never "open" past pay_by, whether or not a window-ending hook is set.
	if cashorder.WindowNotEnded(&order, hubclock.Now().Unix()) {
		return nil, cashorder.ErrWindowNotEnded
	}
	bills, err := svc.orderBills(rec)
	if err != nil {
		return nil, err
	}
	return json.Marshal(cashorder.View(&order, bills))
}

// orderBills are the bills an order minted so far, in minting order, each with
// its token rebuilt byte for byte (in the clear, as the order's reply carries
// it) or its tombstone, the payment it was minted from and, on chain, its
// outpoint.
func (svc *nip47Service) orderBills(rec *db.CashRequestRecord) ([]nipCO.OrderBill, error) {
	rows, err := cashrequest.New(svc.db).Bills(rec.ID)
	if err != nil {
		return nil, err
	}
	now := hubclock.Now()
	out := make([]nipCO.OrderBill, 0, len(rows))
	for i := range rows {
		b := &rows[i]
		if b.Role != db.CashRecordBillOrdered {
			continue
		}
		tok, err := svc.rebuildToken(b, nil, now)
		if err != nil {
			return nil, err
		}
		bill := nipCO.OrderBill{WalletPubkey: b.WalletPubkey, Token: tok, AmountMillis: uint64(max(b.AmountMillis, 0))} //nolint:gosec // non-negative
		if b.PaidMillis != nil {
			bill.PaidMillis = uint64(max(*b.PaidMillis, 0)) //nolint:gosec // non-negative
		}
		if b.Outpoint != nil {
			bill.Outpoint = *b.Outpoint
		}
		out = append(out, bill)
	}
	return out, nil
}
