package postgres

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newOrderId generates a fresh unique identifier so tests sharing testPool
// don't collide with each other's rows.
func newOrderId(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 16)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return hex.EncodeToString(buf)
}

func sampleOrder(orderId string) *core.Lsps1Order {
	return &core.Lsps1Order{
		OrderId:                      orderId,
		PeerId:                       "peer-pubkey",
		LspBalanceLoki:               100_000,
		ClientBalanceLoki:            0,
		FundingConfirmsWithinBlocks:  1,
		RequiredChannelConfirmations: 0,
		ChannelExpiryBlocks:          144,
		Token:                        "",
		AnnounceChannel:              false,
		CreatedAt:                    time.Now(),
		OrderState:                   "CREATED",
		PaymentState:                 "EXPECT_PAYMENT",
		PaymentInvoice:               "lnbc1...",
		OrderTotalLoki:               100_000,
		FeeTotalLoki:                 1_000,
		ExpiresAt:                    time.Now().Add(24 * time.Hour),
		ReservedAmountLoki:           100_000,
	}
}

func TestOpeningStore_RegisterAndGetLsps1Order(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	order := sampleOrder(newOrderId(t))

	require.NoError(t, store.RegisterLsps1Order(order))

	got, err := store.GetLsps1Order(order.OrderId)
	require.NoError(t, err)
	assert.Equal(t, order.PeerId, got.PeerId)
	assert.Equal(t, order.LspBalanceLoki, got.LspBalanceLoki)
	assert.Equal(t, order.OrderState, got.OrderState)
	assert.Equal(t, order.PaymentInvoice, got.PaymentInvoice)
	assert.Equal(t, order.OrderTotalLoki, got.OrderTotalLoki)
	assert.Equal(t, order.FeeTotalLoki, got.FeeTotalLoki)
}

func TestOpeningStore_ListLsps1Orders_RespectsLimitAndOrder(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	var ids []string
	for range 3 {
		o := sampleOrder(newOrderId(t))
		require.NoError(t, store.RegisterLsps1Order(o))
		ids = append(ids, o.OrderId)
		time.Sleep(2 * time.Millisecond) // ensure distinct created_at ordering
	}

	orders, err := store.ListLsps1Orders(2, 0)
	require.NoError(t, err)
	require.Len(t, orders, 2, "limit must be respected")

	// Most recently created (last inserted) must come first.
	assert.Equal(t, ids[2], orders[0].OrderId)
	assert.Equal(t, ids[1], orders[1].OrderId)
}

func TestOpeningStore_CountLsps1Orders_FiltersByStateAndClient(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	peerId := newOrderId(t) // reuse as a unique peer identifier

	created := sampleOrder(newOrderId(t))
	created.PeerId = peerId
	created.OrderState = "CREATED"
	require.NoError(t, store.RegisterLsps1Order(created))

	completed := sampleOrder(newOrderId(t))
	completed.PeerId = peerId
	completed.OrderState = "COMPLETED"
	require.NoError(t, store.RegisterLsps1Order(completed))

	count, err := store.CountLsps1Orders(peerId, "CREATED", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	count, err = store.CountLsps1Orders(peerId, "COMPLETED", nil)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	count, err = store.CountLsps1Orders(peerId, "EXPIRED", nil)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestOpeningStore_CountLsps1Orders_ExcludeExpiredBeforeFilter(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	peerId := newOrderId(t)

	order := sampleOrder(newOrderId(t))
	order.PeerId = peerId
	order.OrderState = "CREATED"
	require.NoError(t, store.RegisterLsps1Order(order))

	future := time.Now().Add(time.Hour)
	count, err := store.CountLsps1Orders(peerId, "CREATED", &future)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "an order created before the cutoff must not count")

	past := time.Now().Add(-time.Hour)
	count, err = store.CountLsps1Orders(peerId, "CREATED", &past)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "an order created after the cutoff must count")
}

func TestOpeningStore_Lsps5Webhooks_RegisterListRemove(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	clientNodeId := newOrderId(t)

	webhook := &core.Lsps5Webhook{
		ClientNodeId: clientNodeId,
		AppName:      "wallet-app",
		Url:          "https://example.com/hook",
		Transport:    "push",
		CreatedAt:    time.Now(),
	}
	require.NoError(t, store.RegisterLsps5Webhook(webhook))

	hooks, err := store.GetLsps5Webhooks(clientNodeId)
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	assert.Equal(t, "wallet-app", hooks[0].AppName)
	assert.Equal(t, "push", hooks[0].Transport)

	count, err := store.CountLsps5Webhooks(clientNodeId)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	require.NoError(t, store.RemoveLsps5Webhook(clientNodeId, "wallet-app"))

	hooks, err = store.GetLsps5Webhooks(clientNodeId)
	require.NoError(t, err)
	assert.Empty(t, hooks)
}

func TestOpeningStore_RegisterLsps5Webhook_UpsertsOnConflict(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	clientNodeId := newOrderId(t)

	first := &core.Lsps5Webhook{
		ClientNodeId: clientNodeId,
		AppName:      "wallet-app",
		Url:          "https://example.com/v1",
		Transport:    "push",
		CreatedAt:    time.Now(),
	}
	require.NoError(t, store.RegisterLsps5Webhook(first))

	second := &core.Lsps5Webhook{
		ClientNodeId: clientNodeId,
		AppName:      "wallet-app", // same (client_node_id, app_name) key
		Url:          "https://example.com/v2",
		Transport:    "nostr",
		CreatedAt:    time.Now(),
	}
	require.NoError(t, store.RegisterLsps5Webhook(second))

	hooks, err := store.GetLsps5Webhooks(clientNodeId)
	require.NoError(t, err)
	require.Len(t, hooks, 1, "same (client_node_id, app_name) must upsert, not duplicate")
	assert.Equal(t, "https://example.com/v2", hooks[0].Url)
	assert.Equal(t, "nostr", hooks[0].Transport)
}

// findByValidity picks out the one setting matching validity, ignoring the
// two global (token IS NULL) default rows the initial migration seeds,
// which GetFeeParamsSettings always includes alongside token-specific ones.
func findByValidity(t *testing.T, settings []*core.OpeningFeeParamsSetting, validity time.Duration) *core.OpeningFeeParamsSetting {
	t.Helper()
	for _, s := range settings {
		if s.Validity == validity {
			return s
		}
	}
	return nil
}

func TestOpeningStore_SetAndGetFeeParamsSettings(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	token := newOrderId(t) // unique token per test
	const validity = 2 * time.Hour

	params := core.OpeningFeeParams{
		MinFeeMloki:          1000,
		Proportional:         4000,
		ValidUntil:           "2099-01-01T00:00:00.000Z",
		MinLifetime:          43200,
		MaxClientToSelfDelay: 4320,
	}
	require.NoError(t, store.SetFeeParams(token, validity, params))

	settings, err := store.GetFeeParamsSettings(token)
	require.NoError(t, err)
	found := findByValidity(t, settings, validity)
	require.NotNil(t, found, "the token-specific setting must be present alongside the global defaults")
	assert.Equal(t, params.MinFeeMloki, found.Params.MinFeeMloki)
	assert.Equal(t, params.Proportional, found.Params.Proportional)
}

func TestOpeningStore_SetFeeParams_ReplacesSameValidityForToken(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)
	token := newOrderId(t)
	const validity = 2 * time.Hour

	original := core.OpeningFeeParams{MinFeeMloki: 1000, Proportional: 4000, ValidUntil: "2099-01-01T00:00:00.000Z"}
	require.NoError(t, store.SetFeeParams(token, validity, original))

	updated := core.OpeningFeeParams{MinFeeMloki: 2000, Proportional: 5000, ValidUntil: "2099-01-01T00:00:00.000Z"}
	require.NoError(t, store.SetFeeParams(token, validity, updated))

	settings, err := store.GetFeeParamsSettings(token)
	require.NoError(t, err)

	var matches int
	for _, s := range settings {
		if s.Validity == validity {
			matches++
		}
	}
	assert.Equal(t, 1, matches, "re-setting the same (token, validity) must replace, not add a second row")

	found := findByValidity(t, settings, validity)
	require.NotNil(t, found)
	assert.Equal(t, uint64(2000), found.Params.MinFeeMloki)
}

func TestOpeningStore_Lsps2BuyRegistration_RoundTrip(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	// SCID values in this codebase can exceed int64 range bit-for-bit
	// (stored as int64 via a cast), so use a realistic-magnitude value.
	scid := uint64(800_000)<<40 | uint64(1234)<<16 | uint64(1)
	paymentSize := uint64(50_000_000)

	reg := &core.Lsps2BuyRegistration{
		Id:                         newOrderId(t),
		LspId:                      "lsp-pubkey",
		PeerId:                     "peer-pubkey",
		Scid:                       scid,
		Mode:                       0,
		PaymentSizeMloki:           &paymentSize,
		ParamsMinFeeMloki:          1000,
		ParamsProportional:         4000,
		ParamsValidUntil:           "2099-01-01T00:00:00.000Z",
		ParamsMinLifetime:          43200,
		ParamsMaxClientToSelfDelay: 4320,
		ParamsPromise:              "deadbeef",
		Token:                      "",
		CreatedAt:                  time.Now(),
		ExpiresAt:                  time.Now().Add(time.Hour),
		ReservedAmountLoki:         50_000,
		State:                      core.Lsps2RegistrationActive,
	}
	require.NoError(t, store.RegisterLsps2Buy(reg))

	got, err := store.GetLsps2BuyRegistration(scid)
	require.NoError(t, err)
	assert.Equal(t, scid, got.Scid)
	assert.Equal(t, reg.LspId, got.LspId)
	assert.Equal(t, reg.PeerId, got.PeerId)
	assert.Equal(t, reg.ParamsMinFeeMloki, got.ParamsMinFeeMloki)
	assert.Equal(t, core.Lsps2RegistrationActive, got.State)
	assert.Equal(t, reg.ReservedAmountLoki, got.ReservedAmountLoki)
}

// TestOpeningStore_Lsps2BuyRegistration_HighBitScid is the actual
// int64/uint64 SCID round-trip regression test: the `scid` column is
// BIGINT (signed int64), and both RegisterLsps2Buy and
// GetLsps2BuyRegistration cast via `int64(scid)`/`uint64(scidInt)`. A SCID
// whose block-height component is >= 2^23 (8,388,608) sets bit 63 of the
// uint64, so int64(scid) is negative — this only round-trips correctly if
// Postgres preserves the exact bit pattern of a negative BIGINT rather than
// e.g. clamping or erroring. The round-trip test above uses block height
// 800,000, which is nowhere near that boundary and doesn't actually
// exercise it.
func TestOpeningStore_Lsps2BuyRegistration_HighBitScid(t *testing.T) {
	store := NewPostgresOpeningStore(testPool)

	const highBlockHeight = 9_000_000 // > 2^23; makes int64(scid) negative
	scid := uint64(highBlockHeight)<<40 | uint64(1)<<16 | uint64(0)
	require.Greater(t, scid, uint64(1)<<63, "fixture must actually set bit 63 to exercise the sign-flip cast")
	require.Negative(t, int64(scid), "sanity: this SCID must be negative when reinterpreted as int64, same as the store does internally")

	paymentSize := uint64(50_000_000)
	reg := &core.Lsps2BuyRegistration{
		Id:                         newOrderId(t),
		LspId:                      "lsp-pubkey-highbit",
		PeerId:                     "peer-pubkey-highbit",
		Scid:                       scid,
		Mode:                       0,
		PaymentSizeMloki:           &paymentSize,
		ParamsMinFeeMloki:          1000,
		ParamsProportional:         4000,
		ParamsValidUntil:           "2099-01-01T00:00:00.000Z",
		ParamsMinLifetime:          43200,
		ParamsMaxClientToSelfDelay: 4320,
		ParamsPromise:              "deadbeef",
		Token:                      "",
		CreatedAt:                  time.Now(),
		ExpiresAt:                  time.Now().Add(time.Hour),
		ReservedAmountLoki:         50_000,
		State:                      core.Lsps2RegistrationActive,
	}
	require.NoError(t, store.RegisterLsps2Buy(reg))

	got, err := store.GetLsps2BuyRegistration(scid)
	require.NoError(t, err)
	assert.Equal(t, scid, got.Scid, "a SCID with bit 63 set must round-trip exactly, not get corrupted by the int64 cast")
}
