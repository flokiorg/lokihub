package postgres

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPaymentStateFor maps a test order_state to a payment_state that's
// actually valid per Lsps1PaymentState's real enum (Phase 7's new
// orders_payment_state_check CHECK constraint enforces this at the DB
// level now, where before there was nothing stopping a test fixture from
// setting payment_state to an order_state-only value like "CREATED" or
// "COMPLETED", neither of which is a real payment state).
func testPaymentStateFor(orderState string) string {
	switch orderState {
	case "CREATED":
		return "EXPECT_PAYMENT"
	case "COMPLETED":
		return "PAID"
	case "PAID", "EXPIRED", "FAILED":
		return orderState
	default:
		return "EXPECT_PAYMENT"
	}
}

func insertTestOrder(t *testing.T, orderId, state string) {
	t.Helper()
	_, err := testPool.Exec(context.Background(),
		`INSERT INTO lsps1.orders
			(order_id, peer_id, lsp_balance_loki, client_balance_loki,
			 funding_confirms_within_blocks, required_channel_confirmations,
			 channel_expiry_blocks, token, announce_channel, created_at,
			 order_state, payment_state, payment_invoice, order_total_loki, fee_total_loki,
			 expires_at, reserved_amount_loki)
		 VALUES ($1, 'peer', 1000, 1000, 1, 0, 100, '', false, now(), $2, $3, 'inv', 1000, 0,
			 now() + interval '24 hours', 2000)
		 ON CONFLICT (order_id) DO UPDATE SET order_state = $2, payment_state = $3`,
		orderId, state, testPaymentStateFor(state))
	require.NoError(t, err)
}

// insertPaidOrder inserts a peer/created_at/funding_txid-configurable PAID
// order, for exercising the FIFO-ordering and "funded orders are excluded"
// logic in GetPaidLsps1Order(s)/GetAllPaidLsps1Orders that insertTestOrder's
// fixed fixture can't.
func insertPaidOrder(t *testing.T, orderId, peerId string, createdAt time.Time, fundingTxid *string) {
	t.Helper()
	_, err := testPool.Exec(context.Background(),
		`INSERT INTO lsps1.orders
			(order_id, peer_id, lsp_balance_loki, client_balance_loki,
			 funding_confirms_within_blocks, required_channel_confirmations,
			 channel_expiry_blocks, token, announce_channel, created_at,
			 order_state, payment_state, payment_invoice, funding_txid, order_total_loki, fee_total_loki,
			 expires_at, reserved_amount_loki)
		 VALUES ($1, $2, 1000, 1000, 1, 0, 100, '', false, $3,
			 'PAID', 'PAID', 'inv', $4, 1000, 0,
			 now() + interval '24 hours', 2000)
		 ON CONFLICT (order_id) DO UPDATE SET peer_id = $2, created_at = $3, funding_txid = $4`,
		orderId, peerId, createdAt, fundingTxid)
	require.NoError(t, err)
}

// insertHeldOrder inserts a PAID/order_state, HOLD/payment_state order --
// the state a hold-invoice order sits in once its HTLC is ACCEPTED but not
// yet settled (see settlement_service.go's processAcceptedOrder). Used to
// prove GetPaidLsps1Order(s) exclude HOLD rows (H-1) while
// GetAllPaidLsps1Orders still includes them (recoverStuckOrders needs to
// see HOLD rows to route them through openChannelForHeldOrder).
func insertHeldOrder(t *testing.T, orderId, peerId string, createdAt time.Time) {
	t.Helper()
	_, err := testPool.Exec(context.Background(),
		`INSERT INTO lsps1.orders
			(order_id, peer_id, lsp_balance_loki, client_balance_loki,
			 funding_confirms_within_blocks, required_channel_confirmations,
			 channel_expiry_blocks, token, announce_channel, created_at,
			 order_state, payment_state, payment_invoice, order_total_loki, fee_total_loki,
			 expires_at, reserved_amount_loki)
		 VALUES ($1, $2, 1000, 1000, 1, 0, 100, '', false, $3,
			 'PAID', 'HOLD', 'inv', 1000, 0,
			 now() + interval '24 hours', 2000)
		 ON CONFLICT (order_id) DO UPDATE SET peer_id = $2, created_at = $3`,
		orderId, peerId, createdAt)
	require.NoError(t, err)
}

// TestGetLsps1OrderByPaymentInvoice_Found covers the happy path for a
// function settlement_service.go's ProcessSettledInvoice relies on to map
// an incoming lnd invoice-settled event back to the LSPS1 order it belongs
// to -- previously had zero test coverage despite being load-bearing.
func TestGetLsps1OrderByPaymentInvoice_Found(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	orderId := newOrderId(t)

	_, err := testPool.Exec(context.Background(),
		`INSERT INTO lsps1.orders
			(order_id, peer_id, lsp_balance_loki, client_balance_loki,
			 funding_confirms_within_blocks, required_channel_confirmations,
			 channel_expiry_blocks, token, announce_channel, created_at,
			 order_state, payment_state, payment_invoice, order_total_loki, fee_total_loki,
			 expires_at, reserved_amount_loki)
		 VALUES ($1, 'peer-invoice-lookup', 1000, 1000, 1, 0, 100, '', false, now(),
			 'CREATED', 'EXPECT_PAYMENT', $2, 1000, 0,
			 now() + interval '24 hours', 2000)`,
		orderId, "lnbc-fixture-invoice-"+orderId)
	require.NoError(t, err)

	order, err := store.GetLsps1OrderByPaymentInvoice("lnbc-fixture-invoice-" + orderId)
	require.NoError(t, err)
	require.NotNil(t, order)
	assert.Equal(t, orderId, order.OrderId)
	assert.Equal(t, "peer-invoice-lookup", order.PeerId)
}

// TestGetLsps1OrderByPaymentInvoice_NotFound documents the current
// contract: unlike PostgresInterceptStore.GetPaidLsps1Order (which
// special-cases pgx.ErrNoRows into (nil, nil)), this method has no such
// handling and returns the raw scan error instead.
func TestGetLsps1OrderByPaymentInvoice_NotFound(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	order, err := store.GetLsps1OrderByPaymentInvoice("no-such-invoice-exists")
	assert.Error(t, err)
	assert.Nil(t, order)
}

// TestUpdateLsps1OrderFunding_UpdatesFields is the regression test for the
// write side of the exact bug class F-8 fixed on the read side: this is
// what settlement_service.go calls right after a JIT/manual channel opens,
// to persist the funding tx so recoverStuckOrders can later tell "funded,
// just needs reconciling" apart from "never actually opened."
func TestUpdateLsps1OrderFunding_UpdatesFields(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	orderId := "funding-update-order"
	insertTestOrder(t, orderId, "PAID")

	claimed, err := store.UpdateLsps1OrderFunding(orderId, "abc123txid", 2)
	require.NoError(t, err)
	assert.True(t, claimed)

	order, err := store.GetLsps1Order(orderId)
	require.NoError(t, err)
	require.NotNil(t, order.FundingTxid)
	assert.Equal(t, "abc123txid", *order.FundingTxid)
	require.NotNil(t, order.FundingOutpoint)
	assert.Equal(t, 2, *order.FundingOutpoint)
}

// TestUpdateLsps1OrderFunding_UnknownOrderIsNotAnError documents the
// current contract: updating a non-existent order_id affects zero rows,
// logs a warning, but does not return an error -- callers relying on this
// to signal "order not found" would be silently misled. It does, however,
// report claimed=false (S-5): zero rows updated means the claim did not
// actually happen, regardless of why.
func TestUpdateLsps1OrderFunding_UnknownOrderIsNotAnError(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	claimed, err := store.UpdateLsps1OrderFunding("no-such-order-id", "txid", 0)
	assert.NoError(t, err, "zero rows affected is logged, not returned as an error")
	assert.False(t, claimed, "zero rows updated means nothing was actually claimed")
}

// TestUpdateLsps1OrderFunding_ConflictingChannelPointReturnsNotClaimed is
// the regression test for S-5: the partial unique index on
// (funding_txid, funding_outpoint) makes claiming a channel point atomic.
// A second order attempting to claim the exact same channel point another
// order already holds must get claimed=false (not an error) -- the
// two-goroutine race variant of this same guarantee is covered by
// TestUpdateLsps1OrderFunding_ConcurrentClaims_ExactlyOneWins below.
func TestUpdateLsps1OrderFunding_ConflictingChannelPointReturnsNotClaimed(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	orderA := "funding-conflict-order-a"
	orderB := "funding-conflict-order-b"
	insertTestOrder(t, orderA, "PAID")
	insertTestOrder(t, orderB, "PAID")

	claimedA, err := store.UpdateLsps1OrderFunding(orderA, "shared-txid", 0)
	require.NoError(t, err)
	require.True(t, claimedA)

	claimedB, err := store.UpdateLsps1OrderFunding(orderB, "shared-txid", 0)
	require.NoError(t, err, "a conflicting claim must not surface as an error")
	assert.False(t, claimedB, "a channel point already claimed by another order must not be claimable again")

	orderBRow, err := store.GetLsps1Order(orderB)
	require.NoError(t, err)
	assert.Nil(t, orderBRow.FundingTxid, "the losing order's funding_txid must be untouched")
}

// TestUpdateLsps1OrderFunding_ConcurrentClaims_ExactlyOneWins proves the
// S-5 guarantee holds under real concurrency, not just sequential calls:
// N goroutines race to claim the SAME channel point for different orders;
// exactly one must succeed.
func TestUpdateLsps1OrderFunding_ConcurrentClaims_ExactlyOneWins(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	const n = 10
	orderIds := make([]string, n)
	for i := range orderIds {
		orderIds[i] = fmt.Sprintf("funding-race-order-%d", i)
		insertTestOrder(t, orderIds[i], "PAID")
	}

	var wg sync.WaitGroup
	var successCount int32
	var mu sync.Mutex
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(orderId string) {
			defer wg.Done()
			claimed, err := store.UpdateLsps1OrderFunding(orderId, "race-shared-txid", 0)
			assert.NoError(t, err)
			if claimed {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(orderIds[i])
	}
	wg.Wait()

	assert.Equal(t, int32(1), successCount, "exactly one of %d concurrent claims on the same channel point must succeed", n)
}

// TestGetAllPaidLsps1Orders_IncludesFundedExcludesOtherStates is the
// regression test for the exact query recoverStuckOrders (F-8/F-10, and
// H-3/H-4 for the hold-invoice case) depends on at startup: every PAID
// order -- across *all* peers, unlike the peer-scoped GetPaidLsps1Order(s)
// -- is a candidate for recovery, funded or not. A PAID order that already
// has a funding_txid MUST still show up here: recoverStuckOrders' own
// Go-level branching (not this query) is what routes a funded order to
// reconcile-instead-of-reopen logic instead of retrying OpenChannel: an
// SQL-level exclusion here would starve that branching of the exact rows
// it exists to handle, silently making the funded-order recovery paths
// (reconcileFundedOrder, reconcileFundedHeldOrder) permanently unreachable
// regardless of how correct their own logic is -- this was a real bug,
// found in review, fixed 2026-07-17 (this query used to exclude funded
// rows; see REVIEW.md's H-3/H-4 fix notes).
func TestGetAllPaidLsps1Orders_IncludesFundedExcludesOtherStates(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	fundingTxid := "already-funded-txid"
	insertPaidOrder(t, "recover-unfunded-peer-a", "peer-a", time.Now(), nil)
	insertPaidOrder(t, "recover-funded-peer-b", "peer-b", time.Now(), &fundingTxid)
	insertTestOrder(t, "recover-created-not-paid", "CREATED")
	insertTestOrder(t, "recover-completed-not-paid", "COMPLETED")

	orders, err := store.GetAllPaidLsps1Orders()
	require.NoError(t, err)

	ids := make(map[string]bool, len(orders))
	for _, o := range orders {
		ids[o.OrderId] = true
	}
	assert.True(t, ids["recover-unfunded-peer-a"], "an unfunded PAID order from any peer must be included")
	assert.True(t, ids["recover-funded-peer-b"], "a funded PAID order must still be included -- recoverStuckOrders' Go-level branching, not this query, is responsible for reconciling it instead of reopening")
	assert.False(t, ids["recover-created-not-paid"], "a CREATED order must be excluded")
	assert.False(t, ids["recover-completed-not-paid"], "a COMPLETED order must be excluded")
}

// TestGetAllCreatedLsps1Orders_OnlyCreatedState is the regression test for
// the other half of startup recovery: orders still awaiting payment,
// checked in case the invoice settled while flspd was down.
func TestGetAllCreatedLsps1Orders_OnlyCreatedState(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	insertTestOrder(t, "created-recovery-a", "CREATED")
	insertTestOrder(t, "created-recovery-paid", "PAID")
	insertTestOrder(t, "created-recovery-expired", "EXPIRED")

	orders, err := store.GetAllCreatedLsps1Orders()
	require.NoError(t, err)

	ids := make(map[string]bool, len(orders))
	for _, o := range orders {
		ids[o.OrderId] = true
	}
	assert.True(t, ids["created-recovery-a"])
	assert.False(t, ids["created-recovery-paid"])
	assert.False(t, ids["created-recovery-expired"])
}

// TestGetPaidLsps1Order_OldestUnfundedForPeer covers PostgresOpeningStore's
// GetPaidLsps1Order -- unused via the ports.OpeningStore interface today
// (the only live call path is PostgresInterceptStore's separate copy in
// intercept_store.go, verified via grep before writing this), but it's
// still public interface surface with its own FIFO/exclusion logic worth
// pinning.
func TestGetPaidLsps1Order_OldestUnfundedForPeer(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	peer := "peer-fifo-single"

	insertPaidOrder(t, "fifo-single-older", peer, time.Now().Add(-time.Hour), nil)
	insertPaidOrder(t, "fifo-single-newer", peer, time.Now(), nil)

	order, err := store.GetPaidLsps1Order(peer)
	require.NoError(t, err)
	require.NotNil(t, order)
	assert.Equal(t, "fifo-single-older", order.OrderId, "must return the oldest unfulfilled PAID order (FIFO)")
}

// TestGetPaidLsps1Orders_AllUnfundedForPeer covers the plural variant:
// every unfunded PAID order for a peer, not just the oldest.
func TestGetPaidLsps1Orders_AllUnfundedForPeer(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	peer := "peer-fifo-plural"
	fundingTxid := "funded"

	insertPaidOrder(t, "fifo-plural-a", peer, time.Now().Add(-2*time.Hour), nil)
	insertPaidOrder(t, "fifo-plural-b", peer, time.Now().Add(-time.Hour), nil)
	insertPaidOrder(t, "fifo-plural-funded", peer, time.Now(), &fundingTxid)

	orders, err := store.GetPaidLsps1Orders(peer)
	require.NoError(t, err)
	require.Len(t, orders, 2, "must return both unfunded orders, excluding the funded one")
	assert.Equal(t, "fifo-plural-a", orders[0].OrderId, "must be FIFO-ordered oldest first")
	assert.Equal(t, "fifo-plural-b", orders[1].OrderId)
}

// TestGetPaidLsps1Order_ExcludesHoldPaymentState is the regression test for
// H-1: a hold-invoice order reaches order_state=PAID the moment its HTLC is
// merely ACCEPTED (payment_state=HOLD), before funds are actually captured.
// GetPaidLsps1Order must not surface it -- doing so is what let the
// interceptor's JIT fallback race settlement_service's own
// openChannelForHeldOrder into opening a duplicate channel for an
// uncaptured payment.
func TestGetPaidLsps1Order_ExcludesHoldPaymentState(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	peer := "peer-hold-exclusion-single"

	insertHeldOrder(t, "hold-exclusion-single", peer, time.Now())

	order, err := store.GetPaidLsps1Order(peer)
	assert.Error(t, err, "no matching PAID/PAID row exists; this method has no pgx.ErrNoRows handling (see TestGetLsps1OrderByPaymentInvoice_NotFound)")
	assert.Nil(t, order, "a HOLD-payment-state order must not be returned as a fully-paid order")
}

// TestGetPaidLsps1Orders_ExcludesHoldPaymentState is the plural counterpart
// of TestGetPaidLsps1Order_ExcludesHoldPaymentState.
func TestGetPaidLsps1Orders_ExcludesHoldPaymentState(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	peer := "peer-hold-exclusion-plural"

	insertPaidOrder(t, "hold-exclusion-plural-settled", peer, time.Now().Add(-time.Hour), nil)
	insertHeldOrder(t, "hold-exclusion-plural-held", peer, time.Now())

	orders, err := store.GetPaidLsps1Orders(peer)
	require.NoError(t, err)
	require.Len(t, orders, 1, "the HOLD order must be excluded, leaving only the genuinely PAID one")
	assert.Equal(t, "hold-exclusion-plural-settled", orders[0].OrderId)
}

// TestGetAllPaidLsps1Orders_IncludesHoldPaymentState confirms the
// interceptor-facing exclusion above does NOT extend to
// GetAllPaidLsps1Orders: recoverStuckOrders relies on this query to see
// HOLD rows too, so it can route them through openChannelForHeldOrder
// (settlement_service.go's recoverStuckOrders branches on PaymentState
// after fetching).
func TestGetAllPaidLsps1Orders_IncludesHoldPaymentState(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	insertHeldOrder(t, "recover-all-held", "peer-recover-all-held", time.Now())

	orders, err := store.GetAllPaidLsps1Orders()
	require.NoError(t, err)

	ids := make(map[string]bool, len(orders))
	for _, o := range orders {
		ids[o.OrderId] = true
	}
	assert.True(t, ids["recover-all-held"], "GetAllPaidLsps1Orders must still include HOLD rows for recoverStuckOrders to find")
}

// TestUpdateLsps1OrderState_CAS_AtSQLLevel proves the WHERE order_state=$4
// clause added for F-1 actually provides atomicity at the database level,
// not just in the Go-level fake-store test in lsps/settlement_service_test.go.
// N goroutines race to transition the SAME real row out of CREATED
// concurrently; exactly one must see updated=true.
func TestUpdateLsps1OrderState_CAS_AtSQLLevel(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	orderId := "cas-sql-test-order"
	insertTestOrder(t, orderId, "CREATED")

	const n = 20
	var wg sync.WaitGroup
	var successCount int32
	var mu sync.Mutex
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			updated, err := store.UpdateLsps1OrderState(orderId, "PAID", "PAID", "CREATED")
			assert.NoError(t, err)
			if updated {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), successCount, "exactly one of %d concurrent CAS attempts on the same row must succeed", n)

	order, err := store.GetLsps1Order(orderId)
	require.NoError(t, err)
	assert.Equal(t, "PAID", order.OrderState)
}

// TestUpdateLsps1OrderState_NoOpWhenStateMismatches confirms the CAS
// correctly refuses to update when the order is no longer in the expected
// state — the mechanism the loser of the F-1 race relies on to safely
// back off instead of double-processing.
func TestUpdateLsps1OrderState_NoOpWhenStateMismatches(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	orderId := "cas-sql-mismatch-order"
	insertTestOrder(t, orderId, "PAID") // already PAID, not CREATED

	updated, err := store.UpdateLsps1OrderState(orderId, "PAID", "PAID", "CREATED")
	require.NoError(t, err)
	assert.False(t, updated, "CAS must refuse to apply when order_state no longer matches expectedCurrentState")

	order, err := store.GetLsps1Order(orderId)
	require.NoError(t, err)
	assert.Equal(t, "PAID", order.OrderState, "order state must be untouched by the failed CAS")
}

// TestGetReservedLiquidity_SumsOnlyRecentCreatedOrders exercises the
// reserved-liquidity SQL directly (Phase 7, F-19): each row's own
// persisted expires_at gates whether it counts, not a hardcoded interval
// -- an order created 48h ago with an expires_at still in the future (a
// non-default, longer-than-24h Lsps1OrderExpirySeconds) must still count,
// which a hardcoded-interval query could never express.
func TestGetReservedLiquidity_SumsOnlyRecentCreatedOrders(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	// testPool is shared across every test in this package (see
	// testmain_test.go), and GetReservedLiquidity is a whole-table sum with
	// no per-test scoping -- other tests' own CREATED/ACTIVE fixtures
	// legitimately contribute to it too. Comparing before/after this test's
	// own inserts isolates the assertion from whatever else exists in the
	// shared DB, rather than asserting a fragile absolute ceiling.
	before, err := store.GetReservedLiquidity()
	require.NoError(t, err)

	_, err = testPool.Exec(context.Background(),
		`INSERT INTO lsps1.orders
			(order_id, peer_id, lsp_balance_loki, client_balance_loki,
			 funding_confirms_within_blocks, required_channel_confirmations,
			 channel_expiry_blocks, token, announce_channel, created_at,
			 order_state, payment_state, payment_invoice, order_total_loki, fee_total_loki,
			 expires_at, reserved_amount_loki)
		 VALUES
		 ('reserved-recent', 'peer', 5000, 0, 1, 0, 100, '', false, now(),
			 'CREATED', 'EXPECT_PAYMENT', 'inv1', 5000, 0, now() + interval '24 hours', 5000),
		 ('reserved-old-but-not-expired', 'peer', 9000, 0, 1, 0, 100, '', false, now() - interval '48 hours',
			 'CREATED', 'EXPECT_PAYMENT', 'inv4', 9000, 0, now() + interval '1 hour', 9000),
		 ('reserved-expired', 'peer', 7000, 0, 1, 0, 100, '', false, now() - interval '48 hours',
			 'CREATED', 'EXPECT_PAYMENT', 'inv2', 7000, 0, now() - interval '24 hours', 7000),
		 ('reserved-completed', 'peer', 3000, 0, 1, 0, 100, '', false, now(),
			 'COMPLETED', 'PAID', 'inv3', 3000, 0, now() - interval '1 hour', 3000)
		 ON CONFLICT (order_id) DO NOTHING`)
	require.NoError(t, err)

	after, err := store.GetReservedLiquidity()
	require.NoError(t, err)
	assert.Equal(t, before+5000+9000, after, "only the two non-expired CREATED orders (including the old-but-not-yet-expired one) may change the sum")
}

// TestGetReservedLiquidity_IncludesPaidOrdersEvenPastExpiresAt is the
// regression test for the LSPS1/LSPS2 PAID-vs-OPENING asymmetry: a PAID
// order has already captured real capital (payment settled/hold invoice
// ACCEPTED) and can sit waiting for its channel-open indefinitely if the
// peer isn't connected -- expires_at was only ever about the payment
// window, not how long committed capital stays reserved, so a PAID order
// must count toward reserved liquidity even once its original expires_at
// has passed. Before this fix, PAID orders were excluded from the sum
// entirely (only CREATED counted), so this capital silently vanished from
// the ledger the moment payment settled.
func TestGetReservedLiquidity_IncludesPaidOrdersEvenPastExpiresAt(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	before, err := store.GetReservedLiquidity()
	require.NoError(t, err)

	_, err = testPool.Exec(context.Background(),
		`INSERT INTO lsps1.orders
			(order_id, peer_id, lsp_balance_loki, client_balance_loki,
			 funding_confirms_within_blocks, required_channel_confirmations,
			 channel_expiry_blocks, token, announce_channel, created_at,
			 order_state, payment_state, payment_invoice, order_total_loki, fee_total_loki,
			 expires_at, reserved_amount_loki)
		 VALUES
		 ('reserved-paid-expired', 'peer', 6000, 0, 1, 0, 100, '', false, now() - interval '48 hours',
			 'PAID', 'PAID', 'inv5', 6000, 0, now() - interval '24 hours', 6000),
		 ('reserved-paid-not-expired', 'peer', 8000, 0, 1, 0, 100, '', false, now(),
			 'PAID', 'PAID', 'inv6', 8000, 0, now() + interval '24 hours', 8000)
		 ON CONFLICT (order_id) DO NOTHING`)
	require.NoError(t, err)

	after, err := store.GetReservedLiquidity()
	require.NoError(t, err)
	assert.Equal(t, before+6000+8000, after, "both PAID orders must count, including the one whose expires_at has already passed")
}

// TestGetReservedLiquidity_IncludesLsps2Registrations is the regression
// test for F-16: LSPS2 buy_registrations must count toward reserved
// liquidity too, not just lsps1.orders. ACTIVE and OPENING both count
// (OPENING is mid-channel-open, still committing real capital); EXPIRED
// and CONSUMED must not.
func TestGetReservedLiquidity_IncludesLsps2Registrations(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	before, err := store.GetReservedLiquidity()
	require.NoError(t, err)

	insertLsps2Registration(t, "reserved-lsps2-active", "ACTIVE", 11_000, time.Now().Add(time.Hour))
	insertLsps2Registration(t, "reserved-lsps2-opening", "OPENING", 13_000, time.Now().Add(time.Hour))
	insertLsps2Registration(t, "reserved-lsps2-expired", "EXPIRED", 17_000, time.Now().Add(-time.Hour))
	insertLsps2Registration(t, "reserved-lsps2-consumed", "CONSUMED", 19_000, time.Now().Add(time.Hour))

	after, err := store.GetReservedLiquidity()
	require.NoError(t, err)
	assert.Equal(t, before+11_000+13_000, after, "only the ACTIVE and OPENING registrations may change the sum -- EXPIRED and CONSUMED must not")
}

// insertLsps2Registration is a minimal fixture for reservation-ledger
// tests that don't care about the full LSPS2 fee-params shape.
func insertLsps2Registration(t *testing.T, idSuffix, state string, reservedLoki uint64, expiresAt time.Time) {
	t.Helper()
	var b [8]byte
	_, err := rand.Read(b[:])
	require.NoError(t, err)
	scid := int64(binary.BigEndian.Uint64(b[:]) & 0x7FFF_FFFF_FFFF_FFFF) // positive int64
	_, err = testPool.Exec(context.Background(),
		`INSERT INTO lsps2.buy_registrations
			(id, lsp_id, peer_id, scid, mode, payment_size_mloki,
			 params_min_fee_mloki, params_proportional, params_valid_until,
			 params_min_lifetime, params_max_client_to_self_delay, params_promise, token,
			 created_at, expires_at, reserved_amount_loki, state)
		 VALUES (gen_random_uuid(), 'lsp', 'peer', $1, 0, NULL, 1000, 100, '2099-01-01T00:00:00.000Z',
			 100, 144, 'promise', '', now(), $2, $3, $4)`,
		scid, expiresAt, reservedLoki, state)
	require.NoError(t, err, "insertLsps2Registration(%s)", idSuffix)
}

// TestExpireStaleOrders_OnlyExpiresOldCreatedOrders exercises
// ExpireStaleOrders's SQL directly (Phase 7, F-19): gated on each row's own
// persisted expires_at, not a hardcoded interval.
func TestExpireStaleOrders_OnlyExpiresOldCreatedOrders(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	_, err := testPool.Exec(context.Background(),
		`INSERT INTO lsps1.orders
			(order_id, peer_id, lsp_balance_loki, client_balance_loki,
			 funding_confirms_within_blocks, required_channel_confirmations,
			 channel_expiry_blocks, token, announce_channel, created_at,
			 order_state, payment_state, payment_invoice, order_total_loki, fee_total_loki,
			 expires_at, reserved_amount_loki)
		 VALUES
		 ('expire-old', 'peer', 1000, 0, 1, 0, 100, '', false, now() - interval '48 hours',
			 'CREATED', 'EXPECT_PAYMENT', 'inv1', 1000, 0, now() - interval '24 hours', 1000),
		 ('expire-recent', 'peer', 1000, 0, 1, 0, 100, '', false, now(),
			 'CREATED', 'EXPECT_PAYMENT', 'inv2', 1000, 0, now() + interval '24 hours', 1000)
		 ON CONFLICT (order_id) DO NOTHING`)
	require.NoError(t, err)

	_, err = store.ExpireStaleOrders()
	require.NoError(t, err)

	oldOrder, err := store.GetLsps1Order("expire-old")
	require.NoError(t, err)
	assert.Equal(t, "EXPIRED", oldOrder.OrderState)

	recentOrder, err := store.GetLsps1Order("expire-recent")
	require.NoError(t, err)
	assert.Equal(t, "CREATED", recentOrder.OrderState, "a recent order must not be expired")
}
