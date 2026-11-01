package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var openingFeeParamsFixture = core.OpeningFeeParams{
	MinFeeMloki:  1000,
	Proportional: 4000,
	ValidUntil:   "2099-01-01T00:00:00.000Z",
}

// TestPaymentInfo_NullOpeningFeeParams is the regression test for F-2: a
// payments row with a NULL opening_fee_params (a plain forward, not a
// channel-open payment — the column is nullable) used to panic PaymentInfo
// with a nil-pointer dereference, because extParams was only assigned
// inside the `if p != nil` branch but dereferenced unconditionally on the
// final return.
func TestPaymentInfo_NullOpeningFeeParams(t *testing.T) {
	store := NewPostgresInterceptStore(testPool)

	paymentHash := []byte("f2-test-null-params-hash-32bytes")
	require.Len(t, paymentHash, 32, "payment_hash is a bytea column keyed by hash; keep it a realistic fixed size")

	_, err := testPool.Exec(context.Background(),
		`INSERT INTO public.payments
			(payment_hash, payment_secret, destination, incoming_amount_mloki, outgoing_amount_mloki, opening_fee_params)
		 VALUES ($1, $2, $3, $4, $5, NULL)
		 ON CONFLICT (payment_hash) DO NOTHING`,
		paymentHash, []byte("secret"), []byte("dest"), int64(1000), int64(990),
	)
	require.NoError(t, err)

	token, orderId, params, gotHash, gotSecret, gotDest, incoming, outgoing, cp, tag, err := store.PaymentInfo(paymentHash)

	assert.NoError(t, err, "PaymentInfo must not error (and, per F-2, must not panic) on a row with NULL opening_fee_params")
	assert.Equal(t, "", token)
	assert.Equal(t, "", orderId)
	assert.Nil(t, params)
	assert.Equal(t, paymentHash, gotHash)
	assert.Equal(t, []byte("secret"), gotSecret)
	assert.Equal(t, []byte("dest"), gotDest)
	assert.Equal(t, int64(1000), incoming)
	assert.Equal(t, int64(990), outgoing)
	assert.Nil(t, cp)
	assert.Nil(t, tag)
}

// TestPaymentInfo_WithOpeningFeeParams is the counterpart happy path: a row
// that DOES have opening_fee_params should still decode correctly, so the
// F-2 fix's early-return-on-nil branch doesn't regress the normal case.
func TestPaymentInfo_WithOpeningFeeParams(t *testing.T) {
	store := NewPostgresInterceptStore(testPool)

	paymentHash := []byte("f2-test-with-params-hash-32bytes")
	require.Len(t, paymentHash, 32)

	err := store.RegisterPayment("token-abc", "order-123", &openingFeeParamsFixture, []byte("dest"), paymentHash, []byte("secret"), 2000, 1990, "")
	require.NoError(t, err)

	token, orderId, params, _, _, _, incoming, outgoing, _, _, err := store.PaymentInfo(paymentHash)

	assert.NoError(t, err)
	assert.Equal(t, "token-abc", token)
	assert.Equal(t, "order-123", orderId)
	require.NotNil(t, params)
	assert.Equal(t, uint64(1000), params.MinFeeMloki)
	assert.Equal(t, int64(2000), incoming)
	assert.Equal(t, int64(1990), outgoing)
}

// TestInterceptStore_GetLsps2Order_RoundTrip covers GetLsps2Order, which
// previously had zero test coverage. It reads the same lsps2.buy_registrations
// table PostgresOpeningStore.RegisterLsps2Buy writes to, via the identical
// int64(scid)/uint64(scidInt) cast — including a SCID with bit 63 set,
// where int64(scid) is negative, to prove the cast round-trips correctly
// from this read path too (see TestOpeningStore_Lsps2BuyRegistration_HighBitScid
// for why that matters).
func TestInterceptStore_GetLsps2Order_RoundTrip(t *testing.T) {
	openingStore := NewPostgresOpeningStore(testPool)
	interceptStore := NewPostgresInterceptStore(testPool)

	const highBlockHeight = 9_500_000 // > 2^23; makes int64(scid) negative
	scid := uint64(highBlockHeight)<<40 | uint64(2)<<16 | uint64(0)
	require.Negative(t, int64(scid), "sanity: fixture must set bit 63")

	paymentSize := uint64(25_000_000)
	reg := &core.Lsps2BuyRegistration{
		Id:                         newOrderId(t),
		LspId:                      "lsp-pubkey-intercept",
		PeerId:                     "peer-pubkey-intercept",
		Scid:                       scid,
		Mode:                       1,
		PaymentSizeMloki:           &paymentSize,
		ParamsMinFeeMloki:          2000,
		ParamsProportional:         5000,
		ParamsValidUntil:           "2099-01-01T00:00:00.000Z",
		ParamsMinLifetime:          43200,
		ParamsMaxClientToSelfDelay: 4320,
		ParamsPromise:              "cafef00d",
		Token:                      "order-token-xyz",
		CreatedAt:                  time.Now(),
		ExpiresAt:                  time.Now().Add(time.Hour),
		ReservedAmountLoki:         25_000,
		State:                      core.Lsps2RegistrationActive,
	}
	require.NoError(t, openingStore.RegisterLsps2Buy(reg))

	got, err := interceptStore.GetLsps2Order(scid)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, scid, got.Scid, "GetLsps2Order must round-trip a bit-63-set SCID exactly")
	assert.Equal(t, reg.LspId, got.LspId)
	assert.Equal(t, reg.PeerId, got.PeerId)
	assert.Equal(t, reg.Token, got.Token)
	assert.Equal(t, reg.ParamsPromise, got.ParamsPromise)
}

// TestInterceptStore_GetLsps2Order_NotFound covers the no-such-SCID branch,
// which must return (nil, nil) rather than an error (callers use a nil
// result to mean "not a JIT-channel payment").
func TestInterceptStore_GetLsps2Order_NotFound(t *testing.T) {
	interceptStore := NewPostgresInterceptStore(testPool)

	got, err := interceptStore.GetLsps2Order(uint64(1)<<62 | 0xdeadbeef)
	assert.NoError(t, err)
	assert.Nil(t, got)
}

// TestSetFundingTx_UpdatesPaymentRow is the regression test for the write
// side of a payment's funding info: interceptor/intercept_handler.go's
// openChannel calls this right after a channel opens, and PaymentInfo's
// returned channelPoint is what the F-8 recovery path (and normal
// duplicate-forward detection) both key off. Previously had zero coverage.
func TestSetFundingTx_UpdatesPaymentRow(t *testing.T) {
	store := NewPostgresInterceptStore(testPool)

	paymentHash := []byte("setfundingtx-test-hash-32-bytes!")
	require.Len(t, paymentHash, 32)
	require.NoError(t, store.RegisterPayment("token", "order-setfundingtx", &openingFeeParamsFixture, []byte("dest"), paymentHash, []byte("secret"), 1000, 990, ""))

	var txHash chainhash.Hash
	copy(txHash[:], []byte("setfundingtx-fixture-txid-32byte"))
	channelPoint := &wire.OutPoint{Hash: txHash, Index: 3}

	err := store.SetFundingTx(paymentHash, channelPoint)
	require.NoError(t, err)

	_, _, _, _, _, _, _, _, gotChannelPoint, _, err := store.PaymentInfo(paymentHash)
	require.NoError(t, err)
	require.NotNil(t, gotChannelPoint, "PaymentInfo must report the channel point SetFundingTx just wrote")
	assert.Equal(t, txHash, gotChannelPoint.Hash)
	assert.Equal(t, uint32(3), gotChannelPoint.Index)
}

// TestInterceptStore_GetPaidLsps1Order_FindsOldestUnfunded covers the live
// production call path (interceptor/intercept_handler.go's resolvePayment,
// via i.store.GetPaidLsps1Order) for falling back to a matching PAID LSPS1
// order when a payment doesn't resolve via the normal payments/lsps2 path.
func TestInterceptStore_GetPaidLsps1Order_FindsOldestUnfunded(t *testing.T) {
	interceptStore := NewPostgresInterceptStore(testPool)
	peer := "peer-intercept-fifo"

	insertPaidOrder(t, "intercept-fifo-older", peer, time.Now().Add(-time.Hour), nil)
	insertPaidOrder(t, "intercept-fifo-newer", peer, time.Now(), nil)

	order, err := interceptStore.GetPaidLsps1Order(peer)
	require.NoError(t, err)
	require.NotNil(t, order)
	assert.Equal(t, "intercept-fifo-older", order.OrderId)
}

// TestInterceptStore_GetPaidLsps1Order_NotFoundReturnsNilNil covers the
// "no matching order" branch, which callers rely on to mean "not an LSPS1
// JIT candidate" rather than treating it as an error -- unlike
// PostgresOpeningStore's separate copy of this query in
// lsps1_settlement_store.go, which has no such pgx.ErrNoRows handling
// (see TestGetPaidLsps1Order_OldestUnfundedForPeer's sibling test for that
// contrast).
func TestInterceptStore_GetPaidLsps1Order_NotFoundReturnsNilNil(t *testing.T) {
	interceptStore := NewPostgresInterceptStore(testPool)

	order, err := interceptStore.GetPaidLsps1Order("no-such-peer-has-orders")
	assert.NoError(t, err)
	assert.Nil(t, order)
}

// TestInterceptStore_GetPaidLsps1Order_ExcludesHoldPaymentState is the
// regression test for H-1 on the actual live production call path (unlike
// PostgresOpeningStore's copy in lsps1_settlement_store.go, which isn't
// wired to any caller today): the interceptor's JIT fallback used to treat
// a HOLD (accepted-but-not-settled) hold-invoice order as fully paid and
// safe to open a channel for, racing settlement_service's own
// openChannelForHeldOrder into a duplicate open for a payment that was
// never actually captured.
func TestInterceptStore_GetPaidLsps1Order_ExcludesHoldPaymentState(t *testing.T) {
	interceptStore := NewPostgresInterceptStore(testPool)
	peer := "peer-intercept-hold-exclusion"

	insertHeldOrder(t, "intercept-hold-exclusion", peer, time.Now())

	order, err := interceptStore.GetPaidLsps1Order(peer)
	require.NoError(t, err)
	assert.Nil(t, order, "a HOLD-payment-state order must not be surfaced as JIT-safe")
}

// TestInterceptStore_GetPaidLsps1Order_ReturnsPreimage is the regression
// test for H-8: this query previously never selected payment_preimage,
// silently returning PaymentPreimage=nil even for hold-invoice orders that
// have one stored, unlike the 7 other Lsps1Order read sites updated
// alongside it in the same commit.
func TestInterceptStore_GetPaidLsps1Order_ReturnsPreimage(t *testing.T) {
	interceptStore := NewPostgresInterceptStore(testPool)
	openingStore := NewPostgresOpeningStore(testPool)
	peer := "peer-intercept-preimage"

	insertPaidOrder(t, "intercept-preimage-order", peer, time.Now(), nil)
	preimage := "deadbeef"
	_, err := testPool.Exec(context.Background(),
		`UPDATE lsps1.orders SET payment_preimage=$2 WHERE order_id=$1`,
		"intercept-preimage-order", preimage)
	require.NoError(t, err)
	// Sanity: confirm the fixture actually has a preimage stored via the
	// already-covered opening-store read path before asserting on the
	// intercept-store one.
	sanity, err := openingStore.GetLsps1Order("intercept-preimage-order")
	require.NoError(t, err)
	require.NotNil(t, sanity.PaymentPreimage)

	order, err := interceptStore.GetPaidLsps1Order(peer)
	require.NoError(t, err)
	require.NotNil(t, order)
	require.NotNil(t, order.PaymentPreimage, "GetPaidLsps1Order must return the stored preimage")
	assert.Equal(t, preimage, *order.PaymentPreimage)
}

func TestRegisterPayment_StoresButNeverLogsThePaymentSecret(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = prev })

	store := NewPostgresInterceptStore(testPool)
	paymentHash := []byte("secret-not-logged-hash-32-bytes!")
	secret := bytes.Repeat([]byte{0x5e}, 32)

	require.NoError(t, store.RegisterPayment("", "", &openingFeeParamsFixture, []byte("dest"), paymentHash, secret, 2000, 1000, ""))

	_, _, _, _, gotSecret, _, _, _, _, _, err := store.PaymentInfo(paymentHash)
	require.NoError(t, err)
	assert.Equal(t, secret, gotSecret, "the secret is stored")
	assert.Contains(t, buf.String(), hex.EncodeToString(paymentHash), "the payment is still logged")
	assert.NotContains(t, buf.String(), hex.EncodeToString(secret))
}
