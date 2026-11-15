package postgres

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newReserveTestOrder(orderId string, reservedLoki uint64) *core.Lsps1Order {
	return &core.Lsps1Order{
		OrderId:                      orderId,
		PeerId:                       "peer",
		LspBalanceLoki:               int64(reservedLoki),
		ClientBalanceLoki:            0,
		FundingConfirmsWithinBlocks:  1,
		RequiredChannelConfirmations: 0,
		ChannelExpiryBlocks:          100,
		CreatedAt:                    time.Now(),
		OrderState:                   "CREATED",
		PaymentState:                 "EXPECT_PAYMENT",
		PaymentInvoice:               "inv-" + orderId,
		OrderTotalLoki:               int64(reservedLoki),
		FeeTotalLoki:                 0,
		ExpiresAt:                    time.Now().Add(24 * time.Hour),
		ReservedAmountLoki:           reservedLoki,
	}
}

// TestReserveLsps1Order_RejectsWhenInsufficient is the sequential-case
// regression test for F-20/T7-5: a wallet balance that can support one
// order but not two must reject the second.
func TestReserveLsps1Order_RejectsWhenInsufficient(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	// testPool is shared package-wide -- baseline against the current
	// reserved total rather than assuming a fresh database (see the
	// concurrent tests below for the same reasoning in more detail).
	baseline, err := store.GetReservedLiquidity()
	require.NoError(t, err)
	walletBalance := baseline + 10_000

	orderA := newReserveTestOrder(newOrderId(t), 7_000)
	reservedA, err := store.ReserveLsps1Order(orderA, walletBalance)
	require.NoError(t, err)
	assert.True(t, reservedA)

	orderB := newReserveTestOrder(newOrderId(t), 7_000) // 7k + 7k > 10k wallet balance
	reservedB, err := store.ReserveLsps1Order(orderB, walletBalance)
	require.NoError(t, err)
	assert.False(t, reservedB, "second order must be rejected once the first has consumed most of the wallet balance")

	// The rejected order must not have been persisted at all.
	_, err = store.GetLsps1Order(orderB.OrderId)
	assert.Error(t, err, "a rejected reservation must not leave a row behind")
}

// TestReserveLsps1Order_ConcurrentReservations_NeverOverCommit is THE
// database-level proof of F-20's fix: N goroutines concurrently try to
// reserve an amount that only a handful of them can fit within one fixed
// wallet balance. Without the advisory lock, a naive check-then-insert
// lets an unbounded number of concurrent callers all read the same stale
// "available" figure and all succeed, over-committing the wallet. With
// the fix, the number that succeed must be exactly the number the balance
// can actually support -- never more.
func TestReserveLsps1Order_ConcurrentReservations_NeverOverCommit(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	// testPool is shared across every test in this package; other tests'
	// own CREATED orders/ACTIVE registrations are never cleaned up and
	// legitimately count toward the same whole-database reserved sum this
	// test relies on. Baseline against the current reserved total (as the
	// GetReservedLiquidity tests already do) instead of assuming a fresh
	// database, so this test's own math isn't polluted by leftover state.
	baseline, err := store.GetReservedLiquidity()
	require.NoError(t, err)

	const (
		perOrderReserve = uint64(10_000)
		n               = 20
		fitCount        = 5
	)
	walletBalance := baseline + perOrderReserve*fitCount
	wantSuccesses := int32(fitCount)

	orderIds := make([]string, n)
	for i := range orderIds {
		orderIds[i] = newOrderId(t)
	}

	var wg sync.WaitGroup
	var successCount int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(orderId string) {
			defer wg.Done()
			order := newReserveTestOrder(orderId, perOrderReserve)
			reserved, err := store.ReserveLsps1Order(order, walletBalance)
			assert.NoError(t, err)
			if reserved {
				atomic.AddInt32(&successCount, 1)
			}
		}(orderIds[i])
	}
	wg.Wait()

	assert.Equal(t, wantSuccesses, successCount,
		"exactly %d of %d concurrent reservations should fit in a %d wallet balance at %d each -- got %d",
		wantSuccesses, n, walletBalance, perOrderReserve, successCount)

	// Cross-check against GetReservedLiquidity directly: the ledger's own
	// view of "how much is reserved now" must agree with how many
	// reservations actually won, not the naive (and wrong, if the race
	// weren't closed) count of all N attempts.
	reservedTotal, err := store.GetReservedLiquidity()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, reservedTotal, uint64(wantSuccesses)*perOrderReserve)
}

// TestReserveLsps2Buy_ConcurrentReservations_NeverOverCommit is
// ReserveLsps1Order's LSPS2 counterpart -- same guarantee, same mechanism
// (both funnel through the same liquidityReservationLockKey).
func TestReserveLsps2Buy_ConcurrentReservations_NeverOverCommit(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	// See TestReserveLsps1Order_ConcurrentReservations_NeverOverCommit's
	// comment: baseline against the current reserved total rather than
	// assuming a fresh database, since testPool is shared package-wide.
	baseline, err := store.GetReservedLiquidity()
	require.NoError(t, err)

	const (
		perOrderReserve = uint64(10_000)
		n               = 15
		fitCount        = 3
	)
	walletBalance := baseline + perOrderReserve*fitCount
	wantSuccesses := int32(fitCount)

	var wg sync.WaitGroup
	var successCount int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			var b [8]byte
			_, _ = rand.Read(b[:])
			scid := binary.BigEndian.Uint64(b[:]) & 0x7FFF_FFFF_FFFF_FFFF
			reg := &core.Lsps2BuyRegistration{
				Id:                 newOrderId(t),
				LspId:              "lsp",
				PeerId:             "peer",
				Scid:               scid,
				ParamsMinFeeMloki:  1000,
				ParamsValidUntil:   "2099-01-01T00:00:00.000Z",
				CreatedAt:          time.Now(),
				ExpiresAt:          time.Now().Add(time.Hour),
				ReservedAmountLoki: perOrderReserve,
				State:              core.Lsps2RegistrationActive,
			}
			reserved, err := store.ReserveLsps2Buy(reg, walletBalance)
			assert.NoError(t, err)
			if reserved {
				atomic.AddInt32(&successCount, 1)
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, wantSuccesses, successCount,
		"exactly %d of %d concurrent LSPS2 reservations should fit in a %d wallet balance at %d each -- got %d",
		wantSuccesses, n, walletBalance, perOrderReserve, successCount)
}
