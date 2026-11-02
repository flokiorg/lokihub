package interceptor

import (
	"testing"
	"time"

	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockStore for InterceptStore interface
type MockStore struct {
	mock.Mock
}

func (m *MockStore) PaymentInfo(paymentHash []byte) (string, string, *core.OpeningFeeParams, []byte, []byte, []byte, int64, int64, *wire.OutPoint, *string, error) {
	args := m.Called(paymentHash)
	var params *core.OpeningFeeParams
	if v := args.Get(2); v != nil {
		params = v.(*core.OpeningFeeParams)
	}
	var gotHash, secret, dest []byte
	if v := args.Get(3); v != nil {
		gotHash = v.([]byte)
	}
	if v := args.Get(4); v != nil {
		secret = v.([]byte)
	}
	if v := args.Get(5); v != nil {
		dest = v.([]byte)
	}
	var channelPoint *wire.OutPoint
	if v := args.Get(8); v != nil {
		channelPoint = v.(*wire.OutPoint)
	}
	var tag *string
	if v := args.Get(9); v != nil {
		tag = v.(*string)
	}
	return args.String(0), args.String(1), params, gotHash, secret, dest, args.Get(6).(int64), args.Get(7).(int64), channelPoint, tag, args.Error(10)
}

func (m *MockStore) GetLsps2Order(scid uint64) (*core.Lsps2BuyRegistration, error) {
	args := m.Called(scid)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*core.Lsps2BuyRegistration), args.Error(1)
}

func (m *MockStore) RegisterPayment(token, orderId string, params *core.OpeningFeeParams, destination, paymentHash, paymentSecret []byte, incomingAmountMloki, outgoingAmountMloki int64, tag string) error {
	args := m.Called(token, orderId, params, destination, paymentHash, paymentSecret, incomingAmountMloki, outgoingAmountMloki, tag)
	return args.Error(0)
}

func (m *MockStore) SetFundingTx(paymentHash []byte, channelPoint *wire.OutPoint) error {
	args := m.Called(paymentHash, channelPoint)
	return args.Error(0)
}

func (m *MockStore) GetPaidLsps1Order(clientNodeId string) (*core.Lsps1Order, error) {
	return nil, nil // Not testing LSPS1 here
}

// ClaimLsps2Registration/ReleaseLsps2Registration/ConsumeLsps2Registration/
// GetStuckLsps2Registrations: Phase 7 (F-15/T7-2). Routed through
// m.Called() like every other method here -- tests that never exercise
// the LSPS2 claim/consume path (isLsps2Registration stays false) never
// trigger these, so they don't need an .On(...) expectation registered.
func (m *MockStore) ClaimLsps2Registration(scid uint64) (bool, error) {
	args := m.Called(scid)
	return args.Bool(0), args.Error(1)
}

func (m *MockStore) ReleaseLsps2Registration(scid uint64) error {
	args := m.Called(scid)
	return args.Error(0)
}

func (m *MockStore) ConsumeLsps2Registration(scid uint64, channelPoint *wire.OutPoint, paymentSizeMloki, feeMloki uint64) (bool, error) {
	args := m.Called(scid, channelPoint, paymentSizeMloki, feeMloki)
	return args.Bool(0), args.Error(1)
}

func (m *MockStore) GetStuckLsps2Registrations() ([]*core.Lsps2BuyRegistration, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*core.Lsps2BuyRegistration), args.Error(1)
}

func (m *MockStore) GetClaimedLsps2ChannelPoints() (map[string]bool, error) {
	args := m.Called()
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]bool), args.Error(1)
}

// TestResolvePayment_JIT_Success tests the happy path where sender pays Gross Amount
func TestResolvePayment_JIT_Success(t *testing.T) {
	mockStore := new(MockStore)
	// Create Interceptor with mock store (other deps nil as not needed for resolvePayment logic logic we test)
	interceptor := &Interceptor{
		store: mockStore,
	}

	// Data
	scid := uint64(123456)
	paymentSize := uint64(20000000) // 20k Loki
	minFee := uint64(1000)
	prop := uint32(4000)

	validUntil := time.Now().Add(1 * time.Hour).Format(time.RFC3339)

	// Mock Order Lookup
	order := &core.Lsps2BuyRegistration{
		Id:                 "order_1",
		Scid:               scid,
		PaymentSizeMloki:   &paymentSize,
		ParamsMinFeeMloki:  minFee,
		ParamsProportional: prop,
		ParamsValidUntil:   validUntil,
		PeerId:             "02deadbeef",
		Token:              "token_1",
	}
	mockStore.On("PaymentInfo", mock.Anything).Return("", "", nil, nil, nil, nil, int64(0), int64(0), nil, nil, nil) // Not found in payments table
	mockStore.On("GetLsps2Order", scid).Return(order, nil)

	// Expect Registration with the BLIP-52 opening fee on the GROSS payment
	// size (FLSPD-0013): fee = ((20_000_000*4000)+999999)/1e6 = 80000 (above
	// the 1000 floor), so outgoing = 20_000_000 - 80000 = 19920000 -- the same
	// fee the buy-time quote reserved, not the old gross-up's 79682/19920318.
	mockStore.On("RegisterPayment",
		"token_1",
		"order_1",
		mock.Anything,   // params
		mock.Anything,   // dest
		mock.Anything,   // hash
		mock.Anything,   // secret
		int64(20000000), // Incoming (Gross)
		int64(19920000), // Outgoing (Net)
		lsps2JitTag,
	).Return(nil)

	// Call
	req := core.InterceptRequest{
		Scid:                lightning.ShortChannelID(scid),
		IncomingAmountMloki: 20000000,
		PaymentHash:         []byte("hash"),
	}

	result, err := interceptor.resolvePayment(req)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, int64(19920000), result.outgoingAmountMloki)
	assert.True(t, result.isJit)
	assert.True(t, result.isLsps2Registration)
	require.NotNil(t, result.tag)
	assert.Equal(t, lsps2JitTag, *result.tag)
}

// TestResolvePayment_JIT_Underpayment tests rejection when sender pays Net Amount
func TestResolvePayment_JIT_Underpayment(t *testing.T) {
	mockStore := new(MockStore)
	interceptor := &Interceptor{store: mockStore}

	scid := uint64(123456)
	paymentSize := uint64(20000000) // 20k

	order := &core.Lsps2BuyRegistration{
		Id:                 "order_1",
		Scid:               scid,
		PaymentSizeMloki:   &paymentSize,
		ParamsMinFeeMloki:  1000,
		ParamsProportional: 4000,
		PeerId:             "02deadbeef",
	}
	mockStore.On("PaymentInfo", mock.Anything).Return("", "", nil, nil, nil, nil, int64(0), int64(0), nil, nil, nil)
	mockStore.On("GetLsps2Order", scid).Return(order, nil)

	// Sender pays only 19920000 (Net)
	req := core.InterceptRequest{
		Scid:                lightning.ShortChannelID(scid),
		IncomingAmountMloki: 19920000,
		PaymentHash:         []byte("hash"),
	}

	result, err := interceptor.resolvePayment(req)

	// Should fail
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "underpayment")
}

// TestResolvePayment_JIT_FixedModeOverpaymentRejected is the FLSPD-0014
// regression test: in fixed-size mode the reserved capacity was sized against
// exactly payment_size at buy time, so an incoming HTLC larger than
// payment_size must be rejected (mirroring the variable-mode worst-case cap)
// rather than opening a channel sized on the larger incoming amount while the
// reservation ledger still only counts payment_size.
func TestResolvePayment_JIT_FixedModeOverpaymentRejected(t *testing.T) {
	mockStore := new(MockStore)
	interceptor := &Interceptor{store: mockStore}

	scid := uint64(123456)
	paymentSize := uint64(20000000) // fixed size the reservation was sized for

	order := &core.Lsps2BuyRegistration{
		Id:                 "order_1",
		Scid:               scid,
		PaymentSizeMloki:   &paymentSize,
		ParamsMinFeeMloki:  1000,
		ParamsProportional: 4000,
		PeerId:             "02deadbeef",
	}
	mockStore.On("PaymentInfo", mock.Anything).Return("", "", nil, nil, nil, nil, int64(0), int64(0), nil, nil, nil)
	mockStore.On("GetLsps2Order", scid).Return(order, nil)

	// Sender pays MORE than the fixed payment_size.
	req := core.InterceptRequest{
		Scid:                lightning.ShortChannelID(scid),
		IncomingAmountMloki: 21000000,
		PaymentHash:         []byte("hash"),
	}

	result, err := interceptor.resolvePayment(req)

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "exceeds fixed payment size")
	// The over-payment must be rejected before any channel/registration is
	// committed -- RegisterPayment is never reached.
	mockStore.AssertNotCalled(t, "RegisterPayment")
}

// TestComputeJitOutgoingAmount covers the fee/floor math independently of
// the mock store, including the case where the proportional fee falls
// below the minFee floor. FLSPD-0013: the fee is now BLIP-52's
// ((size*proportional)+999999)/1e6 on the GROSS payment size (fee=80000 on
// 20M at 4000ppm), not the old gross-up (which charged only 79682); the
// floor-dominated and zero-rate cases are unchanged.
func TestComputeJitOutgoingAmount(t *testing.T) {
	tests := []struct {
		name             string
		paymentSizeMloki int64
		minFeeMloki      int64
		proportional     uint32
		wantOutgoing     int64
		wantFee          int64
	}{
		{
			name:             "proportional fee above floor",
			paymentSizeMloki: 20000000,
			minFeeMloki:      1000,
			proportional:     4000,
			wantOutgoing:     19920000,
			wantFee:          80000,
		},
		{
			name:             "proportional fee below floor, floor applies",
			paymentSizeMloki: 100000,
			minFeeMloki:      1000,
			proportional:     4000,
			wantOutgoing:     99000,
			wantFee:          1000,
		},
		{
			name:             "zero proportional rate",
			paymentSizeMloki: 20000000,
			minFeeMloki:      1000,
			proportional:     0,
			wantOutgoing:     19999000,
			wantFee:          1000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outgoing, fee, err := computeJitOutgoingAmount(tt.paymentSizeMloki, tt.minFeeMloki, tt.proportional)
			require.NoError(t, err)
			assert.Equal(t, tt.wantOutgoing, outgoing, "outgoing mismatch")
			assert.Equal(t, tt.wantFee, fee, "fee mismatch")
		})
	}
}

// TestComputeJitOutgoingAmount_MatchesBuyTimeQuote is the FLSPD-0013
// regression test: the fee charged at JIT redemption must equal the fee the
// buy-time quote computes via lsps.ComputeOpeningFee, since a client's
// reservation was sized against exactly that figure. Both must be the gross
// BLIP-52 fee, not the old outgoing-amount gross-up.
func TestComputeJitOutgoingAmount_MatchesBuyTimeQuote(t *testing.T) {
	cases := []struct {
		paymentSize  uint64
		minFee       uint64
		proportional uint32
	}{
		{20_000_000, 1000, 4000},
		{10_000_000, 100, 10_000},
		{1_000_000, 750, 0},
		{100_000, 1000, 4000}, // floor dominates
	}
	for _, c := range cases {
		quoted, err := lsps.ComputeOpeningFee(c.paymentSize, c.minFee, uint64(c.proportional))
		require.NoError(t, err)

		_, jitFee, err := computeJitOutgoingAmount(int64(c.paymentSize), int64(c.minFee), c.proportional)
		require.NoError(t, err)

		assert.Equal(t, int64(quoted), jitFee, "JIT fee must equal the buy-time quoted/reserved fee for size=%d prop=%d", c.paymentSize, c.proportional)
	}
}

// Capacity-sizing behavior (padding, network floor, and the reserve floor
// that guarantees outgoingAmountMloki is actually forwardable) is covered
// by the real-call tests in open_channel_test.go
// (TestOpenChannel_Success_NonJitUsesConfiguredRoutingFee,
// TestOpenChannel_RejectsCapacityBelowNetworkFloor,
// TestOpenChannel_CapacityLeavesRoomForChannelReserve), which exercise
// openChannel itself rather than a standalone copy of its math.
