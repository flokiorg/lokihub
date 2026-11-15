package cashorder

import (
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// OrderRow is one placed order as the operator console lists it (NIP-CO, WP-19
// item 8). It is operator-facing and carries NO secret: no cash token, no
// buyer key — only the order's own facts and the metadata of its payments.
type OrderRow struct {
	ID            uint              `json:"id"`
	State         string            `json:"state"`
	Payment       string            `json:"payment"`
	AmountMillis  int64             `json:"amount_millis"`
	FeeMillis     int64             `json:"fee_millis"`
	PayMillis     int64             `json:"pay_millis"`
	PayBy         int64             `json:"pay_by"`
	Deadline      int64             `json:"deadline"`
	Confirmations *int              `json:"confirmations,omitempty"`
	RecipientType string            `json:"recipient_type"`
	CreatedAt     int64             `json:"created_at"`
	StateAt       int64             `json:"state_at"`
	Payments      []OrderPaymentRow `json:"payments"`
}

// OrderPaymentRow is one payment to an order (NIP-CO §Paying and Minting): its
// state, value, confirmations and timestamps. A minted payment is a minted
// bill; the bill's token stays with the buyer and is never exposed here.
type OrderPaymentRow struct {
	State         string `json:"state"`
	ValueMillis   int64  `json:"value_millis"`
	Confirmations int    `json:"confirmations"`
	Address       string `json:"address,omitempty"`
	SeenAt        int64  `json:"seen_at"`
	MintedAt      *int64 `json:"minted_at,omitempty"`
}

// KeptPaymentRow is a payment the Hub kept with no order (WP-19 item 8): at
// most the fee, or a payment to an order address that matched no live order.
type KeptPaymentRow struct {
	ValueMillis int64  `json:"value_millis"`
	State       string `json:"state"`
	SeenAt      int64  `json:"seen_at"`
}

// OrdersView is a Cash Hub's placed orders (newest first) and the payments it
// kept with no order, for the operator console. It holds no secret.
type OrdersView struct {
	Orders []OrderRow       `json:"orders"`
	Kept   []KeptPaymentRow `json:"kept"`
}

// ListOrders lists a Cash Hub's placed orders newest first (capped at limit),
// each with its payment metadata, plus the payments the Hub kept with no order.
// It exposes operator facts only — never a cash token or a buyer key (the token
// of a minted bill stays with its buyer; WP-19 item 8).
func ListOrders(gdb *gorm.DB, hubID uint, limit int) (*OrdersView, error) {
	if limit <= 0 {
		limit = 500
	}
	var orders []db.CashOrder
	if err := gdb.Where("hub_app_id = ?", hubID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&orders).Error; err != nil {
		return nil, err
	}
	ids := make([]uint, len(orders))
	for i := range orders {
		ids[i] = orders[i].ID
	}
	byOrder := make(map[uint][]OrderPaymentRow, len(ids))
	if len(ids) > 0 {
		var pays []db.CashOrderPayment
		if err := gdb.Where("order_id IN ?", ids).Order("seen_at").Find(&pays).Error; err != nil {
			return nil, err
		}
		for i := range pays {
			p := &pays[i]
			if p.OrderID == nil {
				continue
			}
			row := OrderPaymentRow{State: p.State, ValueMillis: p.ValueMillis,
				Confirmations: p.Confirmations, SeenAt: p.SeenAt, MintedAt: p.MintedAt}
			if p.Address != nil {
				row.Address = *p.Address
			}
			byOrder[*p.OrderID] = append(byOrder[*p.OrderID], row)
		}
	}
	out := &OrdersView{Orders: make([]OrderRow, 0, len(orders))}
	for i := range orders {
		o := &orders[i]
		// A freshly placed order has no payments yet; return an empty array,
		// never a nil slice (which marshals to JSON null and breaks the UI).
		payments := byOrder[o.ID]
		if payments == nil {
			payments = []OrderPaymentRow{}
		}
		out.Orders = append(out.Orders, OrderRow{
			ID: o.ID, State: o.State, Payment: o.Payment,
			AmountMillis: o.AmountMillis, FeeMillis: o.FeeMillis, PayMillis: o.PayMillis,
			PayBy: o.PayBy, Deadline: o.Deadline, Confirmations: o.Confirmations,
			RecipientType: o.RecipientType, CreatedAt: o.CreatedAt, StateAt: o.StateAt,
			Payments: payments,
		})
	}
	var kept []db.CashOrderPayment
	if err := gdb.Where("hub_app_id = ? AND order_id IS NULL", hubID).
		Order("seen_at DESC").Limit(limit).Find(&kept).Error; err != nil {
		return nil, err
	}
	out.Kept = make([]KeptPaymentRow, 0, len(kept))
	for i := range kept {
		k := &kept[i]
		out.Kept = append(out.Kept, KeptPaymentRow{ValueMillis: k.ValueMillis, State: k.State, SeenAt: k.SeenAt})
	}
	return out, nil
}
