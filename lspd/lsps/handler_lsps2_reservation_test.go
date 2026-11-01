package lsps

import (
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func newLsps2ReservationTestServer(t *testing.T, minChan uint64) (*Handlers, *MockNodesService, *MockOpeningService, *MockLiquidityManager, *btcec.PublicKey) {
	t.Helper()
	mockLnd := new(MockLndClient)
	mockNodes := new(MockNodesService)
	mockOpening := new(MockOpeningService)
	mockLiquidity := new(MockLiquidityManager)
	mockNotification := new(MockNotificationService)

	privKey, pubKey := btcec.PrivKeyFromBytes([]byte("testprivkey1111111111111111111"))
	nodeId, _ := hex.DecodeString("033333333333333333333333333333333333333333333333333333333333333333")
	node := &core.Node{
		NodeId:     nodeId,
		PublicKey:  pubKey,
		PrivateKey: privKey,
		Tokens:     []string{"test-token"},
		NodeConfig: config.NewNodeConfigRef(&config.NodeConfig{TimeLockDelta: 144, JitEnabled: true}),
	}

	mockNodes.On("GetNodes").Return([]*core.Node{node})
	mockNodes.On("GetMinChannelCapacity").Return(minChan)
	mockOpening.On("ValidateOpeningFeeParams", mock.AnythingOfType("*core.OpeningFeeParams"), pubKey).Return(true)

	server := NewHandlers(Config{Lsps1MaxPendingOrders: 5, Lsps5MaxWebhooks: 10}, mockLnd, mockNodes, mockOpening, mockLiquidity, mockNotification, nil)
	return server, mockNodes, mockOpening, mockLiquidity, pubKey
}

func buyRequest(t *testing.T, minFeeMloki string, proportional uint32, validUntil string, minPayment, maxPayment, paymentSize string) *JsonRpcRequest {
	t.Helper()
	buyParams := Lsps2BuyParams{
		OpeningFeeParams: Lsps2OpeningFeeParams{
			MinFeeMloki:          minFeeMloki,
			Proportional:         proportional,
			ValidUntil:           validUntil,
			MinLifetime:          10080,
			MaxClientToSelfDelay: 2016,
			MinPaymentSizeMloki:  minPayment,
			MaxPaymentSizeMloki:  maxPayment,
			Promise:              "valid_promise",
		},
		PaymentSizeMloki: paymentSize,
	}
	paramsBytes, _ := json.Marshal(buyParams)
	return &JsonRpcRequest{Method: "lsps2.buy", Params: paramsBytes, Id: "1"}
}

// TestLsps2Buy_F22_NoPaymentSize_StillChecksLiquidity is the regression
// test for F-22: before the fix, omitting payment_size_mloki (BLIP-52's
// spec-legal "variable size, single HTLC" mode) skipped every
// capacity/liquidity check entirely. It must now be checked against the
// quote's own max_payment_size_mloki as the worst case, and rejected when
// that worst case would be insufficient -- not silently accepted.
func TestLsps2Buy_F22_NoPaymentSize_StillChecksLiquidity(t *testing.T) {
	server, _, _, mockLiquidity, _ := newLsps2ReservationTestServer(t, 20_000)
	validUntil := time.Now().Add(1 * time.Hour).Format(time.RFC3339)

	// max_payment_size_mloki (100_000_000 mloki = 100_000 loki) worst case
	// must be what's checked -- zero available liquidity rejects any
	// non-zero requested capacity, proving the bypass is closed.
	mockLiquidity.On("GetLiquiditySnapshot", mock.Anything).Return(LiquiditySnapshot{}, nil)

	req := buyRequest(t, "1000", 100, validUntil, "1000", "100000000", "" /* no payment_size_mloki */)
	resp := server.Route(req, []byte("peer_id"))

	require.NotNil(t, resp.Error, "F-22: a buy with no payment_size_mloki must still be liquidity-checked, not silently accepted")
	mockLiquidity.AssertCalled(t, "GetLiquiditySnapshot", mock.Anything)
}

// TestLsps2Buy_F22_NoPaymentSize_NoMaxEither_Rejected covers the
// degenerate case: no payment_size_mloki AND no max_payment_size_mloki on
// the quote leaves no bound to check at all -- must be rejected cleanly
// (not registered with a zero/undefined reservation).
func TestLsps2Buy_F22_NoPaymentSize_NoMaxEither_Rejected(t *testing.T) {
	server, _, mockOpening, _, _ := newLsps2ReservationTestServer(t, 20_000)
	validUntil := time.Now().Add(1 * time.Hour).Format(time.RFC3339)

	req := buyRequest(t, "1000", 100, validUntil, "0", "0", "")
	resp := server.Route(req, []byte("peer_id"))

	require.NotNil(t, resp.Error)
	mockOpening.AssertNotCalled(t, "RegisterLsps2Buy", mock.Anything)
}

// TestLsps2Buy_PersistsReservationFields is the regression test for
// T7-1/T7-2/F-16: a registered buy must carry State=ACTIVE, a
// ReservedAmountLoki that reflects the real committed capacity (not the
// raw mloki payment size -- R-2's off-by-1000 risk), and an ExpiresAt no
// later than the quote's own valid_until.
func TestLsps2Buy_PersistsReservationFields(t *testing.T) {
	server, _, _, mockLiquidity, _ := newLsps2ReservationTestServer(t, 20_000)
	validUntil := time.Now().UTC().Add(1 * time.Hour)

	mockSufficientLiquidity(mockLiquidity)

	var captured *core.Lsps2BuyRegistration
	mockLiquidity.On("ReserveLsps2Buy", mock.Anything, mock.MatchedBy(func(r *core.Lsps2BuyRegistration) bool {
		captured = r
		return true
	})).Return(true, nil)

	req := buyRequest(t, "1000", 500, validUntil.Format(time.RFC3339), "1000", "100000000", "50000000")
	resp := server.Route(req, []byte("peer_id"))
	require.Nil(t, resp.Error)
	require.NotNil(t, captured)

	assert.Equal(t, core.Lsps2RegistrationActive, captured.State)
	// 50_000_000 mloki = 50_000 loki -- reserved figure must be in that
	// ballpark (loki, not the raw mloki number, and not 1000x either
	// direction -- R-2's exact concern), not zero.
	assert.Greater(t, captured.ReservedAmountLoki, uint64(0))
	assert.Less(t, captured.ReservedAmountLoki, uint64(1_000_000), "reserved amount must be in loki, not mloki (R-2: off-by-1000 risk)")
	assert.WithinDuration(t, validUntil, captured.ExpiresAt, time.Second, "ExpiresAt must track the quote's own valid_until when it's inside the max TTL ceiling")
	assert.False(t, captured.CreatedAt.IsZero())
}

// TestLsps2Buy_PersistsMaxPaymentSizeMloki_FixedSizeMode and
// TestLsps2Buy_PersistsMaxPaymentSizeMloki_VariableSizeMode are the
// regression tests for F-22's redemption-time half: the worst-case bound
// computed at buy time (worstCaseMloki) must be persisted on the
// registration (MaxPaymentSizeMloki), not just used to size
// ReservedAmountLoki and discarded -- otherwise the interceptor's
// resolvePayment has nothing to enforce against the real incoming HTLC
// amount at redemption.
func TestLsps2Buy_PersistsMaxPaymentSizeMloki_FixedSizeMode(t *testing.T) {
	server, _, _, mockLiquidity, _ := newLsps2ReservationTestServer(t, 20_000)
	validUntil := time.Now().UTC().Add(1 * time.Hour)
	mockSufficientLiquidity(mockLiquidity)

	var captured *core.Lsps2BuyRegistration
	mockLiquidity.On("ReserveLsps2Buy", mock.Anything, mock.MatchedBy(func(r *core.Lsps2BuyRegistration) bool {
		captured = r
		return true
	})).Return(true, nil)

	req := buyRequest(t, "1000", 500, validUntil.Format(time.RFC3339), "1000", "100000000", "50000000")
	resp := server.Route(req, []byte("peer_id"))
	require.Nil(t, resp.Error)
	require.NotNil(t, captured)

	require.NotNil(t, captured.MaxPaymentSizeMloki)
	assert.Equal(t, uint64(50000000), *captured.MaxPaymentSizeMloki, "fixed-size mode's bound must equal the client's own quoted payment_size_mloki")
}

func TestLsps2Buy_PersistsMaxPaymentSizeMloki_VariableSizeMode(t *testing.T) {
	server, _, _, mockLiquidity, _ := newLsps2ReservationTestServer(t, 20_000)
	validUntil := time.Now().UTC().Add(1 * time.Hour)
	mockSufficientLiquidity(mockLiquidity)

	var captured *core.Lsps2BuyRegistration
	mockLiquidity.On("ReserveLsps2Buy", mock.Anything, mock.MatchedBy(func(r *core.Lsps2BuyRegistration) bool {
		captured = r
		return true
	})).Return(true, nil)

	// No payment_size_mloki -- variable-size mode.
	req := buyRequest(t, "1000", 500, validUntil.Format(time.RFC3339), "1000", "100000000", "")
	resp := server.Route(req, []byte("peer_id"))
	require.Nil(t, resp.Error)
	require.NotNil(t, captured)

	require.NotNil(t, captured.MaxPaymentSizeMloki)
	assert.Equal(t, uint64(100000000), *captured.MaxPaymentSizeMloki, "variable-size mode's bound must equal the quote's own max_payment_size_mloki")
}

// TestLsps2Buy_ExpiresAtCappedByMaxTTL is the regression test for F-21:
// a registration must never outlive a hard ceiling even if the quote's own
// valid_until is set far in the future.
func TestLsps2Buy_ExpiresAtCappedByMaxTTL(t *testing.T) {
	server, _, _, mockLiquidity, _ := newLsps2ReservationTestServer(t, 20_000)
	farFuture := time.Now().UTC().Add(7 * 24 * time.Hour) // a week out

	mockSufficientLiquidity(mockLiquidity)

	var captured *core.Lsps2BuyRegistration
	mockLiquidity.On("ReserveLsps2Buy", mock.Anything, mock.MatchedBy(func(r *core.Lsps2BuyRegistration) bool {
		captured = r
		return true
	})).Return(true, nil)

	req := buyRequest(t, "1000", 500, farFuture.Format(time.RFC3339), "1000", "100000000", "50000000")
	resp := server.Route(req, []byte("peer_id"))
	require.Nil(t, resp.Error)
	require.NotNil(t, captured)

	assert.True(t, captured.ExpiresAt.Before(farFuture), "ExpiresAt must be capped well before a far-future valid_until")
	assert.WithinDuration(t, time.Now().Add(lsps2RegistrationMaxTTL), captured.ExpiresAt, 5*time.Second)
}
