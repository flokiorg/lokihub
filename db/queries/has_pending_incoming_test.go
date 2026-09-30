package queries_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	testdb "github.com/flokiorg/lokihub/tests/db"
)

// HasPendingIncoming decides whether a sub-wallet may be destroyed. Both
// directions are dangerous and neither is a tie-breaker:
//
//   - saying yes when nothing can settle pins the wallet, its parent hub and
//     (for a circle) its identity forever, because the caller's only recourse is
//     to "try again shortly" and that never becomes true;
//   - saying no while a payment is genuinely in flight deletes the transaction
//     row the settlement needs, and the funds have nowhere to credit.
//
// So each case below states which of those two it is guarding.
func seedIncoming(t *testing.T, gormDB *gorm.DB, appID uint, state string, expiresAt *time.Time) {
	t.Helper()
	require.NoError(t, gormDB.Create(&db.Transaction{
		AppId:     &appID,
		Type:      constants.TRANSACTION_TYPE_INCOMING,
		State:     state,
		ExpiresAt: expiresAt,
	}).Error)
}

func newWalletApp(t *testing.T, gormDB *gorm.DB, name string) uint {
	t.Helper()
	app := db.App{Name: name, AppPubkey: name}
	require.NoError(t, gormDB.Create(&app).Error)
	return app.ID
}

func TestHasPendingIncoming(t *testing.T) {
	ago := func(d time.Duration) *time.Time { at := time.Now().Add(-d); return &at }
	ahead := func(d time.Duration) *time.Time { at := time.Now().Add(d); return &at }

	tests := []struct {
		name      string
		state     string
		expiresAt *time.Time
		want      bool
		why       string
	}{
		{
			name:      "PendingAndStillPayable",
			state:     constants.TRANSACTION_STATE_PENDING,
			expiresAt: ahead(time.Hour),
			want:      true,
			why:       "an unexpired invoice can still be paid, and deleting the wallet would drop the row its settlement needs",
		},
		{
			name:      "PendingWithNoExpiry",
			state:     constants.TRANSACTION_STATE_PENDING,
			expiresAt: nil,
			want:      true,
			why:       "no known deadline is not evidence that nothing can settle; the guard protects funds, so an unknown must not authorize a delete",
		},
		{
			name:      "PendingJustPastExpiryIsStillWithinGrace",
			state:     constants.TRANSACTION_STATE_PENDING,
			expiresAt: ago(time.Minute),
			want:      true,
			why:       "an HTLC accepted moments before expiry settles just after it, and the notification can lag; the grace window covers that gap",
		},
		{
			name:      "PendingLongExpired",
			state:     constants.TRANSACTION_STATE_PENDING,
			expiresAt: ago(48 * time.Hour),
			want:      false,
			why:       "an invoice two days past expiry can never be paid, so reporting it as still settling pins the wallet permanently — this is the leak that stranded 54 apps and 27 circle identities",
		},
		{
			name:      "AcceptedHoldInvoice",
			state:     constants.TRANSACTION_STATE_ACCEPTED,
			expiresAt: ahead(time.Hour),
			want:      true,
			why:       "a held HTLC is a payment genuinely in flight and will credit on settle; this state was previously not counted at all, defeating the guard for the exact case it exists to protect",
		},
		{
			name:      "AcceptedHoldInvoicePastInvoiceExpiry",
			state:     constants.TRANSACTION_STATE_ACCEPTED,
			expiresAt: ago(48 * time.Hour),
			want:      true,
			why:       "once an HTLC is held it must be settled or cancelled regardless of the invoice's expiry, so the expiry filter must not reach ACCEPTED rows",
		},
		{
			name:      "SettledDoesNotCount",
			state:     constants.TRANSACTION_STATE_SETTLED,
			expiresAt: nil,
			want:      false,
			why:       "already credited; nothing is in flight",
		},
		{
			name:      "FailedDoesNotCount",
			state:     constants.TRANSACTION_STATE_FAILED,
			expiresAt: nil,
			want:      false,
			why:       "will never credit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gormDB, err := testdb.NewDB(t)
			require.NoError(t, err)

			appID := newWalletApp(t, gormDB, "wallet-"+tt.name)
			seedIncoming(t, gormDB, appID, tt.state, tt.expiresAt)

			require.Equal(t, tt.want, queries.HasPendingIncoming(gormDB, appID), tt.why)
		})
	}
}

// TestHasPendingIncoming_OneLivePaymentAmongDeadOnesStillBlocks is the case a
// per-row rewrite is most likely to get wrong: the answer is about the SET, and
// one payment that can still settle has to outvote any number that cannot.
func TestHasPendingIncoming_OneLivePaymentAmongDeadOnesStillBlocks(t *testing.T) {
	gormDB, err := testdb.NewDB(t)
	require.NoError(t, err)

	appID := newWalletApp(t, gormDB, "wallet-mixed")
	longExpired := time.Now().Add(-48 * time.Hour)
	live := time.Now().Add(time.Hour)

	seedIncoming(t, gormDB, appID, constants.TRANSACTION_STATE_PENDING, &longExpired)
	seedIncoming(t, gormDB, appID, constants.TRANSACTION_STATE_PENDING, &longExpired)
	seedIncoming(t, gormDB, appID, constants.TRANSACTION_STATE_PENDING, &live)

	require.True(t, queries.HasPendingIncoming(gormDB, appID),
		"two dead invoices must not mask the one that can still be paid")
}

// TestHasPendingIncoming_IgnoresOtherAppsPayments pins the scoping. Shared here
// because a wallet's deletability must not depend on its siblings.
func TestHasPendingIncoming_IgnoresOtherAppsPayments(t *testing.T) {
	gormDB, err := testdb.NewDB(t)
	require.NoError(t, err)

	mine := newWalletApp(t, gormDB, "wallet-mine")
	theirs := newWalletApp(t, gormDB, "wallet-theirs")

	live := time.Now().Add(time.Hour)
	seedIncoming(t, gormDB, theirs, constants.TRANSACTION_STATE_PENDING, &live)

	require.False(t, queries.HasPendingIncoming(gormDB, mine),
		"another app's in-flight payment must not block this wallet")
}
