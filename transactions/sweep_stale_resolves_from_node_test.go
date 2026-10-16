package transactions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// The stale-pending sweep used to mark every outgoing row PENDING for 48 h as
// FAILED without asking the node (a notification-capable backend skips the
// invoice lookup). Failing a payment that went out credits the payer back with
// money that has left. Each case below resolves the row from what the node (or,
// for a self-payment, its own incoming leg) says, and only that.

const staleSweepHash = "e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5e5"

func createStaleOutgoing(t *testing.T, svc *tests.TestService, selfPayment bool) db.Transaction {
	t.Helper()
	staleTime := time.Now().Add(-72 * time.Hour)
	row := db.Transaction{
		State:           constants.TRANSACTION_STATE_PENDING,
		Type:            constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash:     staleSweepHash,
		AmountMloki:     123000,
		FeeReserveMloki: 10000,
		SelfPayment:     selfPayment,
	}
	require.NoError(t, svc.DB.Create(&row).Error)
	require.NoError(t, svc.DB.Model(&row).Update("created_at", staleTime).Error)
	return row
}

func sweepAndReload(t *testing.T, svc *tests.TestService, ln lnclient.LNClient, id uint) db.Transaction {
	t.Helper()
	NewTransactionsService(svc.DB, svc.EventPublisher).SweepStalePendingOutgoing(context.Background(), ln)
	var refreshed db.Transaction
	require.NoError(t, svc.DB.First(&refreshed, id).Error)
	return refreshed
}

func newSweepService(t *testing.T) (*tests.TestService, *tests.MockLn) {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	return svc, svc.LNClient.(*tests.MockLn)
}

func TestSweepStalePendingOutgoing_NodeSettled_MarkedSettled(t *testing.T) {
	svc, mockLn := newSweepService(t)
	row := createStaleOutgoing(t, svc, false)
	mockLn.PaymentLookups = map[string]*lnclient.PaymentLookup{
		staleSweepHash: {Status: lnclient.PaymentStatusSucceeded, Preimage: "stale-sweep-preimage", FeeMloki: 7},
	}

	got := sweepAndReload(t, svc, mockLn, row.ID)
	require.Equal(t, constants.TRANSACTION_STATE_SETTLED, got.State)
	require.NotNil(t, got.Preimage)
	require.Equal(t, "stale-sweep-preimage", *got.Preimage)
	require.EqualValues(t, 7, got.FeeMloki)
}

func TestSweepStalePendingOutgoing_NodeFailed_MarkedFailed(t *testing.T) {
	svc, mockLn := newSweepService(t)
	row := createStaleOutgoing(t, svc, false)
	mockLn.PaymentLookups = map[string]*lnclient.PaymentLookup{
		staleSweepHash: {Status: lnclient.PaymentStatusFailed, FailureReason: "FAILURE_REASON_NO_ROUTE"},
	}

	got := sweepAndReload(t, svc, mockLn, row.ID)
	require.Equal(t, constants.TRANSACTION_STATE_FAILED, got.State)
	require.Equal(t, "FAILURE_REASON_NO_ROUTE", got.FailureReason)
}

func TestSweepStalePendingOutgoing_OutcomeUnknown_LeftPending(t *testing.T) {
	cases := map[string]func(*tests.MockLn){
		"lookup error": func(m *tests.MockLn) { m.PaymentLookupError = errors.New("node unreachable") },
		"in flight":    func(m *tests.MockLn) {}, // the mock reports an unknown hash in flight
		"succeeded without preimage": func(m *tests.MockLn) {
			m.PaymentLookups = map[string]*lnclient.PaymentLookup{staleSweepHash: {Status: lnclient.PaymentStatusSucceeded}}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			svc, mockLn := newSweepService(t)
			row := createStaleOutgoing(t, svc, false)
			setup(mockLn)

			got := sweepAndReload(t, svc, mockLn, row.ID)
			require.Equal(t, constants.TRANSACTION_STATE_PENDING, got.State,
				"a payment that may have gone out is never failed by age alone")
		})
	}
}

// noLookupLn is a backend that cannot look outgoing payments up.
type noLookupLn struct{ lnclient.LNClient }

func TestSweepStalePendingOutgoing_BackendCannotLookUp_LeftPending(t *testing.T) {
	svc, mockLn := newSweepService(t)
	row := createStaleOutgoing(t, svc, false)

	got := sweepAndReload(t, svc, noLookupLn{mockLn}, row.ID)
	require.Equal(t, constants.TRANSACTION_STATE_PENDING, got.State)
}

func TestSweepStalePendingOutgoing_SelfPayment_ResolvedFromIncomingLeg(t *testing.T) {
	cases := []struct {
		name     string
		incoming string // "" = no incoming leg
		want     string
	}{
		{"incoming settled", constants.TRANSACTION_STATE_SETTLED, constants.TRANSACTION_STATE_SETTLED},
		{"incoming failed", constants.TRANSACTION_STATE_FAILED, constants.TRANSACTION_STATE_FAILED},
		{"no incoming leg", "", constants.TRANSACTION_STATE_FAILED},
		{"incoming still open", constants.TRANSACTION_STATE_PENDING, constants.TRANSACTION_STATE_PENDING},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, mockLn := newSweepService(t)
			row := createStaleOutgoing(t, svc, true)
			// The node has no record of a self-payment; that must not fail it.
			mockLn.PaymentLookups = map[string]*lnclient.PaymentLookup{
				staleSweepHash: {Status: lnclient.PaymentStatusFailed},
			}
			if tc.incoming != "" {
				preimage := "self-leg-preimage"
				require.NoError(t, svc.DB.Create(&db.Transaction{
					State: tc.incoming, Type: constants.TRANSACTION_TYPE_INCOMING,
					PaymentHash: staleSweepHash, AmountMloki: 123000, Preimage: &preimage, SelfPayment: true,
				}).Error)
			}

			got := sweepAndReload(t, svc, mockLn, row.ID)
			require.Equal(t, tc.want, got.State)
			if tc.want == constants.TRANSACTION_STATE_SETTLED {
				require.Equal(t, "self-leg-preimage", *got.Preimage)
			}
		})
	}
}
