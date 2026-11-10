package cashorder_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cash/cashorder"
	"github.com/flokiorg/lokihub/db"
	testsdb "github.com/flokiorg/lokihub/tests/db"
)

func ptrI(v int) *int       { return &v }
func ptrI64(v int64) *int64 { return &v }
func ptrU(v uint) *uint     { return &v }
func ptrS(v string) *string { return &v }

// TestListOrders_ListsHubOrdersWithPaymentsAndKept covers WP-19 item 8: the
// operator orders list returns a Cash Hub's placed orders (newest first) with
// their payment metadata, plus the payments the Hub kept with no order — and
// only for that Hub.
func TestListOrders_ListsHubOrdersWithPaymentsAndKept(t *testing.T) {
	gdb, err := testsdb.NewDB(t)
	require.NoError(t, err)

	// Two orders on hub 1 (B placed after A), one order on hub 2 (isolation).
	require.NoError(t, gdb.Create(&db.CashOrder{
		RequestID: "req-a", HubAppID: 1, OrderConnPubkey: "cc", BuyerPubkey: "bb",
		RecipientType: "pubkey", AmountMillis: 1000, Payment: "ln", TermsJSON: "{}",
		FeeMillis: 10, PayMillis: 990, PayBy: 100, Deadline: 200,
		State: db.CashOrderMinted, CreatedAt: 100, StateAt: 150,
	}).Error)
	require.NoError(t, gdb.Create(&db.CashOrder{
		RequestID: "req-b", HubAppID: 1, OrderConnPubkey: "cc", BuyerPubkey: "bb",
		RecipientType: "cash", AmountMillis: 500, Payment: "onchain", TermsJSON: "{}",
		FeeMillis: 5, PayMillis: 495, PayBy: 300, Deadline: 400, Confirmations: ptrI(2),
		State: db.CashOrderOpen, CreatedAt: 200, StateAt: 250,
	}).Error)
	require.NoError(t, gdb.Create(&db.CashOrder{
		RequestID: "req-other", HubAppID: 2, OrderConnPubkey: "cc", BuyerPubkey: "bb",
		RecipientType: "pubkey", AmountMillis: 7, Payment: "ln", TermsJSON: "{}",
		State: db.CashOrderOpen, CreatedAt: 300, StateAt: 300,
	}).Error)

	// Payments: one minted on order A, one seen on order B, one kept by hub 1.
	require.NoError(t, gdb.Create(&db.CashOrderPayment{
		OrderID: ptrU(1), PaymentKey: "ln:hashA", ValueMillis: 1000,
		SeenAt: 110, State: db.CashOrderPaymentMinted, MintedAt: ptrI64(150),
	}).Error)
	require.NoError(t, gdb.Create(&db.CashOrderPayment{
		OrderID: ptrU(2), PaymentKey: "oc:txb:0", Address: ptrS("flc1qaddr"), ValueMillis: 500,
		Confirmations: 1, SeenAt: 210, State: db.CashOrderPaymentSeen,
	}).Error)
	require.NoError(t, gdb.Create(&db.CashOrderPayment{
		HubAppID: ptrU(1), PaymentKey: "oc:txk:0", ValueMillis: 50,
		SeenAt: 260, State: db.CashOrderPaymentKept,
	}).Error)

	view, err := cashorder.ListOrders(gdb, 1, 500)
	require.NoError(t, err)

	// Newest first: B (created 200) then A (created 100). Hub 2's order absent.
	require.Len(t, view.Orders, 2)
	assert.EqualValues(t, 500, view.Orders[0].AmountMillis)
	assert.Equal(t, "onchain", view.Orders[0].Payment)
	assert.Equal(t, db.CashOrderOpen, view.Orders[0].State)
	require.NotNil(t, view.Orders[0].Confirmations)
	assert.Equal(t, 2, *view.Orders[0].Confirmations)
	assert.EqualValues(t, 1000, view.Orders[1].AmountMillis)
	assert.Equal(t, db.CashOrderMinted, view.Orders[1].State)

	// Payment metadata is attached to the right order.
	require.Len(t, view.Orders[0].Payments, 1)
	assert.Equal(t, db.CashOrderPaymentSeen, view.Orders[0].Payments[0].State)
	assert.Equal(t, "flc1qaddr", view.Orders[0].Payments[0].Address)
	require.Len(t, view.Orders[1].Payments, 1)
	assert.Equal(t, db.CashOrderPaymentMinted, view.Orders[1].Payments[0].State)
	require.NotNil(t, view.Orders[1].Payments[0].MintedAt)

	// The payment kept with no order shows up under Kept, not under an order.
	require.Len(t, view.Kept, 1)
	assert.EqualValues(t, 50, view.Kept[0].ValueMillis)
	assert.Equal(t, db.CashOrderPaymentKept, view.Kept[0].State)

	// Security invariant: the operator view exposes no cash token / secret.
	blob, err := json.Marshal(view)
	require.NoError(t, err)
	for _, forbidden := range []string{"token", "secret", "pairing"} {
		assert.NotContains(t, strings.ToLower(string(blob)), forbidden,
			"operator orders view must not expose %q", forbidden)
	}
}

// TestListOrders_OrderWithNoPaymentsIsEmptyArray guards against a nil Payments
// slice (JSON null) for a freshly placed, unpaid order — the UI reads
// order.payments.length and would crash on null.
func TestListOrders_OrderWithNoPaymentsIsEmptyArray(t *testing.T) {
	gdb, err := testsdb.NewDB(t)
	require.NoError(t, err)
	require.NoError(t, gdb.Create(&db.CashOrder{
		RequestID: "req-unpaid", HubAppID: 1, OrderConnPubkey: "cc", BuyerPubkey: "bb",
		RecipientType: "pubkey", AmountMillis: 1000, Payment: "ln", TermsJSON: "{}",
		State: db.CashOrderOpen, CreatedAt: 100, StateAt: 100,
	}).Error)

	view, err := cashorder.ListOrders(gdb, 1, 500)
	require.NoError(t, err)
	require.Len(t, view.Orders, 1)
	assert.NotNil(t, view.Orders[0].Payments)
	assert.Empty(t, view.Orders[0].Payments)

	blob, err := json.Marshal(view)
	require.NoError(t, err)
	assert.Contains(t, string(blob), `"payments":[]`)
	assert.NotContains(t, string(blob), `"payments":null`)
}

// TestListOrders_EmptyHub returns empty slices (not nil) for a Hub with nothing.
func TestListOrders_EmptyHub(t *testing.T) {
	gdb, err := testsdb.NewDB(t)
	require.NoError(t, err)
	view, err := cashorder.ListOrders(gdb, 42, 500)
	require.NoError(t, err)
	assert.NotNil(t, view.Orders)
	assert.NotNil(t, view.Kept)
	assert.Empty(t, view.Orders)
	assert.Empty(t, view.Kept)
}
