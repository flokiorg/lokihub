package nip47

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipCO"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashorder"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CO §Minting: each payment is minted exactly once, however often and however concurrently it is taken in or minted.
func TestOrderIntake_PaymentMintedOnceUnderConcurrentIntakes(t *testing.T) {
	f := newIntakeFixture(t)
	res, _, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	o := f.orderRow(t, p.RequestID)
	settled := f.clock.Unix()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.engine.IntakeLightning(context.TODO(), &lnclient.Transaction{PaymentHash: *o.PaymentHash, Amount: o.PayMillis,
				AmountPaid: o.PayMillis, SettledAt: &settled})
		}()
	}
	wg.Wait()
	var pay db.CashOrderPayment
	require.NoError(t, f.svc.DB.Where("order_id = ?", o.ID).First(&pay).Error)
	// A second and third mint of the same, already minted payment mint nothing.
	require.NoError(t, f.engine.MintPayment(context.TODO(), pay.ID))
	require.NoError(t, f.engine.MintPayment(context.TODO(), pay.ID))
	var claims int64
	require.NoError(t, f.svc.DB.Model(&db.CashWalletClaim{}).Count(&claims).Error)
	assert.EqualValues(t, 1, claims)
	var payments int64
	require.NoError(t, f.svc.DB.Model(&db.CashOrderPayment{}).Count(&payments).Error)
	assert.EqualValues(t, 1, payments)
	assert.Equal(t, db.CashOrderMinted, f.orderRow(t, p.RequestID).State)
}

// NIP-CO §What It Allows: a retired Order connection still serves cash_lookup for its orders and still mints their payments.
func TestCashOrder_RetiredConnectionStillLooksUpAndMints(t *testing.T) {
	f := newIntakeFixture(t)
	res, buyer, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	o := f.orderRow(t, p.RequestID)
	require.NoError(t, cashorder.RetireConnection(cashorder.Deps{DB: f.svc.DB}, f.hub.ID, f.conn.AppID, time.Now()))
	refused, _, _ := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	require.NotNil(t, refused.Error, "a retired connection takes no new order")
	assert.Equal(t, constants.ERROR_RESTRICTED, refused.Error.Code)
	assert.Equal(t, nipCO.OrderOpen, f.lookupOrder(t, buyer, p).State)
	require.True(t, f.settle(t, o, o.PayMillis))
	got := f.lookupOrder(t, buyer, p)
	assert.Equal(t, nipCO.OrderMinted, got.State)
	require.Len(t, got.Bills, 1)
	assert.EqualValues(t, 21_000, got.Bills[0].AmountMillis)
}

// NIP-CO §Request: a cash_order carrying recipients, expiry, cash_secret, idempotency_key or min_transfer_millis is BAD_REQUEST, and leaves no order.
func TestCashOrder_ForbiddenKeysRefusedAndNothingPlaced(t *testing.T) {
	f := newOrderFixture(t)
	recipient, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	for _, key := range []string{"recipients", "expiry", "cash_secret", "idempotency_key", "min_transfer_millis"} {
		params := map[string]any{
			"recipient":     map[string]any{"identity_type": "pubkey", "identity_value": recipient},
			"amount_millis": 21_000, "payment": "lightning", "notify": strings.Repeat("ef", 32),
			"request_id": tests.RandomHex32(), key: 1,
		}
		res, served := f.serve(t, func(b nipcash.ItemBinding) (transport.Item, error) {
			return nipcash.BuildItem("o", f.conn.WalletPubkey, f.secret, nipcash.MethodCashOrder, params,
				nipcash.BySigning(nostr.GeneratePrivateKey()), b)
		})
		require.True(t, served, key)
		require.NotNil(t, res.Error, key)
		assert.Equal(t, constants.ERROR_BAD_REQUEST, res.Error.Code, key)
	}
	var n int64
	require.NoError(t, f.svc.DB.Model(&db.CashOrder{}).Count(&n).Error)
	assert.Zero(t, n)

	res, _, _ := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	require.Nil(t, res.Error, "the same order without a forbidden key is placed")
}

// NIP-CO §Lightning and §Minting: a settlement the Hub never heard of is minted when the window ends (the invoice reads settled), never expired.
func TestOrderIntake_MissedSettlementMintedAtPayBy(t *testing.T) {
	f := newIntakeFixture(t)
	res, _, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	o := f.orderRow(t, p.RequestID)
	settled := f.clock.Unix()
	f.ln.MockTransaction = &lnclient.Transaction{PaymentHash: *o.PaymentHash, Amount: o.PayMillis, AmountPaid: o.PayMillis,
		SettledAt: &settled, Preimage: tests.RandomHex32()}
	f.pastPayBy(t, p.RequestID)
	f.engine.RunPayBy(context.TODO())
	assert.Equal(t, db.CashOrderMinted, f.orderRow(t, p.RequestID).State)
	var claims int64
	require.NoError(t, f.svc.DB.Model(&db.CashWalletClaim{}).Count(&claims).Error)
	assert.EqualValues(t, 1, claims)
}

// NIP-CO §Minting: a settlement missed while the Hub was down is taken in by the startup sweep while the order is still open.
func TestOrderIntake_StartupSweepMintsAMissedSettlement(t *testing.T) {
	f := newIntakeFixture(t)
	res, _, p := f.order(t, nipCO.PaymentLightning, 21_000, nil)
	orderOf(t, res)
	o := f.orderRow(t, p.RequestID)
	settled := f.clock.Unix()
	f.ln.MockTransaction = &lnclient.Transaction{PaymentHash: *o.PaymentHash, Amount: o.PayMillis, AmountPaid: o.PayMillis + 5000,
		SettledAt: &settled, Preimage: tests.RandomHex32()}
	f.engine.SweepLightning(context.TODO())
	assert.Equal(t, db.CashOrderMinted, f.orderRow(t, p.RequestID).State)
	var amt int64
	require.NoError(t, f.svc.DB.Model(&db.CashWalletClaim{}).Pluck("amount_mloki", &amt).Error)
	assert.EqualValues(t, 21_000+5000, amt, "what was paid, less the fee")
}

// NIP-CO §Limits: concurrent orders never take the open orders of a payment kind past the open-order ceiling.
func TestCashOrder_CeilingHoldsUnderConcurrentPlacements(t *testing.T) {
	f := newOrderFixture(t)
	s := orderTermsFixture()
	s.OpenOnchainMax = 0 // only the amount ceiling (2,000,000) bounds them
	require.NoError(t, cashorder.PutSettings(f.svc.DB, f.hub.ID, s, time.Now()))
	const n = 5
	items := make([]func(nipcash.ItemBinding) (transport.Item, error), n)
	for i := range n {
		f.ln.OnchainAddresses = append(f.ln.OnchainAddresses, "fcrt1q"+tests.RandomHex32()[:38])
		buyer := nostr.GeneratePrivateKey()
		notify, err := nipCO.NewNotify()
		require.NoError(t, err)
		recipient, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
		p := nipCO.CashOrderParams{Recipient: nipcash.Pubkey(recipient), AmountMillis: 1_000_000, Payment: nipCO.PaymentOnchain,
			Notify: notify, RequestID: tests.RandomHex32()}
		items[i] = func(b nipcash.ItemBinding) (transport.Item, error) {
			return p.Item("o", f.conn.WalletPubkey, f.secret, buyer, b)
		}
	}
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.serve(t, items[i])
		}()
	}
	wg.Wait()
	var sum int64
	require.NoError(t, f.svc.DB.Model(&db.CashOrder{}).Select("COALESCE(SUM(reserved_millis),0)").Where("payment = ?", nipCO.PaymentOnchain).Scan(&sum).Error)
	assert.LessOrEqual(t, sum, int64(2_000_000))
	var placed int64
	require.NoError(t, f.svc.DB.Model(&db.CashOrder{}).Where("payment = ?", nipCO.PaymentOnchain).Count(&placed).Error)
	assert.EqualValues(t, 2, placed, "the ceiling fits exactly two")
}

// NIP-CO §On-chain: an address is given to one order only, even when the node hands it out again.
func TestCashOrder_AddressNeverGivenTwice(t *testing.T) {
	f := newOrderFixture(t)
	addr := "fcrt1q" + tests.RandomHex32()[:38]
	res, _, _ := f.order(t, nipCO.PaymentOnchain, 10_000, nil)
	first := orderOf(t, res).Address
	// The node repeats the first order's address for every later request.
	f.ln.OnchainAddresses = []string{first, first, first}
	f.ln.OnchainAddresses = append(f.ln.OnchainAddresses, addr)
	res, _, _ = f.order(t, nipCO.PaymentOnchain, 10_000, nil)
	if res.Error == nil {
		assert.NotEqual(t, first, orderOf(t, res).Address)
	}
	var n int64
	require.NoError(t, f.svc.DB.Model(&db.CashOrder{}).Where("address = ?", first).Count(&n).Error)
	assert.EqualValues(t, 1, n)
}
