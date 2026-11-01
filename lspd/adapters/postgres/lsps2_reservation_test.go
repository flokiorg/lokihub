package postgres

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestLsps2Registration inserts a fresh ACTIVE registration and returns
// its scid, for tests exercising the Claim/Consume/Release lifecycle.
func newTestLsps2Registration(t *testing.T, state core.Lsps2RegistrationState, expiresAt time.Time) uint64 {
	t.Helper()
	openingStore := NewPostgresOpeningStore(testPool)
	var b [8]byte
	_, err := rand.Read(b[:])
	require.NoError(t, err)
	scid := binary.BigEndian.Uint64(b[:]) & 0x7FFF_FFFF_FFFF_FFFF

	paymentSize := uint64(20_000_000)
	reg := &core.Lsps2BuyRegistration{
		Id:                 newOrderId(t),
		LspId:              "lsp-pubkey",
		PeerId:             "peer-pubkey",
		Scid:               scid,
		Mode:               0,
		PaymentSizeMloki:   &paymentSize,
		ParamsMinFeeMloki:  1000,
		ParamsProportional: 100,
		ParamsValidUntil:   "2099-01-01T00:00:00.000Z",
		ParamsMinLifetime:  100,
		Token:              "",
		CreatedAt:          time.Now(),
		ExpiresAt:          expiresAt,
		ReservedAmountLoki: 20_000,
		State:              core.Lsps2RegistrationActive,
	}
	require.NoError(t, openingStore.RegisterLsps2Buy(reg))

	if state != core.Lsps2RegistrationActive {
		_, err := testPool.Exec(context.Background(),
			`UPDATE lsps2.buy_registrations SET state=$1 WHERE scid=$2`, string(state), int64(scid))
		require.NoError(t, err)
	}
	return scid
}

// TestClaimLsps2Registration_OnlyActiveAndUnexpiredCanBeClaimed is the
// table-driven regression test for F-15/T7-2's claim gate: ACTIVE and
// not-yet-expired is the only state a real channel-open may proceed from.
func TestClaimLsps2Registration_OnlyActiveAndUnexpiredCanBeClaimed(t *testing.T) {
	store := NewPostgresInterceptStore(testPool)

	tests := []struct {
		name        string
		state       core.Lsps2RegistrationState
		expiresAt   time.Time
		wantClaimed bool
	}{
		{"ACTIVE and not expired", core.Lsps2RegistrationActive, time.Now().Add(time.Hour), true},
		{"ACTIVE but expired", core.Lsps2RegistrationActive, time.Now().Add(-time.Hour), false},
		{"already OPENING", core.Lsps2RegistrationOpening, time.Now().Add(time.Hour), false},
		{"already CONSUMED", core.Lsps2RegistrationConsumed, time.Now().Add(time.Hour), false},
		{"already EXPIRED", core.Lsps2RegistrationExpired, time.Now().Add(time.Hour), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scid := newTestLsps2Registration(t, tt.state, tt.expiresAt)
			claimed, err := store.ClaimLsps2Registration(scid)
			require.NoError(t, err)
			assert.Equal(t, tt.wantClaimed, claimed)
		})
	}
}

// TestClaimLsps2Registration_ConcurrentClaims_ExactlyOneWins is THE
// database-level proof of F-15's fix: N goroutines race to claim the SAME
// registration concurrently (simulating a sequential replay racing a
// genuinely concurrent distinct-payment-hash HTLC); exactly one may
// proceed to openChannel. Mirrors
// TestUpdateLsps1OrderFunding_ConcurrentClaims_ExactlyOneWins's existing
// pattern for the identical class of guarantee.
func TestClaimLsps2Registration_ConcurrentClaims_ExactlyOneWins(t *testing.T) {
	store := NewPostgresInterceptStore(testPool)
	scid := newTestLsps2Registration(t, core.Lsps2RegistrationActive, time.Now().Add(time.Hour))

	const n = 20
	var wg sync.WaitGroup
	var successCount int32
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			claimed, err := store.ClaimLsps2Registration(scid)
			assert.NoError(t, err)
			if claimed {
				atomic.AddInt32(&successCount, 1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), successCount, "exactly one of %d concurrent claims on the same registration must succeed", n)
}

// TestReleaseLsps2Registration_AllowsRetryAfterOpenChannelFailure covers
// the OPENING->ACTIVE revert path: after a failed channel-open, the
// registration must become claimable again.
func TestReleaseLsps2Registration_AllowsRetryAfterOpenChannelFailure(t *testing.T) {
	store := NewPostgresInterceptStore(testPool)
	scid := newTestLsps2Registration(t, core.Lsps2RegistrationActive, time.Now().Add(time.Hour))

	claimed, err := store.ClaimLsps2Registration(scid)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, store.ReleaseLsps2Registration(scid))

	reClaimed, err := store.ClaimLsps2Registration(scid)
	require.NoError(t, err)
	assert.True(t, reClaimed, "a released registration must be claimable again for a retry")
}

// TestConsumeLsps2Registration_RecordsBoughtChannel is the regression test
// for F-15/F-21: consuming a registration must both flip its state to
// CONSUMED and write a matching lsps2.bought_channels row -- the table has
// existed in the schema since the original migration but nothing wrote to
// it before Phase 7.
func TestConsumeLsps2Registration_RecordsBoughtChannel(t *testing.T) {
	store := NewPostgresInterceptStore(testPool)
	scid := newTestLsps2Registration(t, core.Lsps2RegistrationActive, time.Now().Add(time.Hour))

	claimed, err := store.ClaimLsps2Registration(scid)
	require.NoError(t, err)
	require.True(t, claimed)

	var hash chainhash.Hash
	copy(hash[:], []byte("consume-test-funding-txid-32byte"))
	channelPoint := &wire.OutPoint{Hash: hash, Index: 0}

	consumed, err := store.ConsumeLsps2Registration(scid, channelPoint, 20_000_000, 1_000)
	require.NoError(t, err)
	assert.True(t, consumed)

	var state string
	var boughtCount int
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT state FROM lsps2.buy_registrations WHERE scid=$1`, int64(scid)).Scan(&state))
	assert.Equal(t, "CONSUMED", state)

	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM lsps2.bought_channels bc
		 JOIN lsps2.buy_registrations br ON br.id = bc.registration_id
		 WHERE br.scid = $1 AND bc.is_completed = true AND bc.fee_mloki = 1000 AND bc.payment_size_mloki = 20000000`,
		int64(scid)).Scan(&boughtCount))
	assert.Equal(t, 1, boughtCount, "exactly one matching bought_channels row must exist")
}

// TestConsumeLsps2Registration_NotOpeningReturnsFalse proves a second
// attempt to consume an already-consumed (or never-claimed) registration
// is a clean no-op, not an error or a duplicate bought_channels row -- the
// scenario a concurrent duplicate HTLC racing the winner would hit.
func TestConsumeLsps2Registration_NotOpeningReturnsFalse(t *testing.T) {
	store := NewPostgresInterceptStore(testPool)
	scid := newTestLsps2Registration(t, core.Lsps2RegistrationActive, time.Now().Add(time.Hour))

	var hash chainhash.Hash
	copy(hash[:], []byte("second-attempt-funding-txid-32by"))
	channelPoint := &wire.OutPoint{Hash: hash, Index: 0}

	// Never claimed (still ACTIVE) -- consume must refuse.
	consumed, err := store.ConsumeLsps2Registration(scid, channelPoint, 1000, 100)
	require.NoError(t, err)
	assert.False(t, consumed, "consuming a registration that was never claimed (still ACTIVE, not OPENING) must not succeed")

	var boughtCount int
	require.NoError(t, testPool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM lsps2.bought_channels bc
		 JOIN lsps2.buy_registrations br ON br.id = bc.registration_id
		 WHERE br.scid = $1`, int64(scid)).Scan(&boughtCount))
	assert.Equal(t, 0, boughtCount)
}

// TestGetStuckLsps2Registrations_ReturnsOnlyOpening is the fixture-level
// regression test for T7-13: crash recovery must find registrations left
// at OPENING (a crash between openChannel succeeding and the
// ConsumeLsps2Registration commit), and only those.
func TestGetStuckLsps2Registrations_ReturnsOnlyOpening(t *testing.T) {
	store := NewPostgresInterceptStore(testPool)
	stuckScid := newTestLsps2Registration(t, core.Lsps2RegistrationOpening, time.Now().Add(time.Hour))
	newTestLsps2Registration(t, core.Lsps2RegistrationActive, time.Now().Add(time.Hour))
	newTestLsps2Registration(t, core.Lsps2RegistrationConsumed, time.Now().Add(time.Hour))
	newTestLsps2Registration(t, core.Lsps2RegistrationExpired, time.Now().Add(time.Hour))

	stuck, err := store.GetStuckLsps2Registrations()
	require.NoError(t, err)

	found := false
	for _, reg := range stuck {
		assert.Equal(t, core.Lsps2RegistrationOpening, reg.State, "every returned registration must be OPENING")
		if reg.Scid == stuckScid {
			found = true
		}
	}
	assert.True(t, found, "the fixture's OPENING registration must be present in the result")
}
