package transactions

// AUDIT-D QA / D-QA-1 — the closing test for "nothing binds a reconciliation to
// its own invoice".
//
// tests.MockLn.LookupInvoice accepts a paymentHash and discards it: whatever is
// in MockTransaction comes back for any hash at all. Every existing test of
// checkUnsettledTransaction / SweepStalePendingOutgoing therefore passes with the
// lookup pointed at a constant — demonstrated by mutating
// transactions_service.go:939 to
//
//	lnClient.LookupInvoice(ctx, "deadbeef-not-this-transactions-hash")
//
// after which `go test ./... -count=1 -p 1` still returned EXIT=0 across all 56
// packages.
//
// That matters because checkUnsettledTransaction writes the looked-up invoice's
// preimage and FeesPaid onto the row it is reconciling. A lookup bound to the
// wrong hash marks transaction A settled out of transaction B's state.
//
// This file closes the gap WITHOUT touching the tracked mock: hashBoundLn embeds
// *tests.MockLn and overrides the one method, so it still satisfies
// lnclient.LNClient in full.
//
// bug-present (mutant): "reconciled against hash X, but this transaction's hash is Y"
// bug-absent  (HEAD):   pass.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// hashBoundLn answers LookupInvoice ONLY for the hash it was told about, and
// records every hash it was asked for. A real node behaves this way; the shared
// mock does not.
type hashBoundLn struct {
	*tests.MockLn

	mu     sync.Mutex
	asked  []string
	only   string
	answer *lnclient.Transaction
}

func (ln *hashBoundLn) LookupInvoice(ctx context.Context, paymentHash string) (*lnclient.Transaction, error) {
	ln.mu.Lock()
	ln.asked = append(ln.asked, paymentHash)
	ln.mu.Unlock()
	if paymentHash != ln.only {
		// A real node has no invoice under a hash it never issued.
		return nil, errors.New("no invoice under payment hash " + paymentHash)
	}
	return ln.answer, nil
}

func (ln *hashBoundLn) asksSoFar() []string {
	ln.mu.Lock()
	defer ln.mu.Unlock()
	return append([]string(nil), ln.asked...)
}

func TestAuditDQA_CheckUnsettledTransaction_ReconcilesAgainstItsOwnHash(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A hash that is NOT any of the package's mock constants, so a fixture that
	// answers "whatever I was seeded with" cannot accidentally be right.
	const ownHash = "d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1d1"

	dbTransaction := db.Transaction{
		State:       constants.TRANSACTION_STATE_PENDING,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: ownHash,
		AmountMloki: 123000,
		CreatedAt:   time.Now(),
	}
	require.NoError(t, svc.DB.Create(&dbTransaction).Error)

	mock := svc.LNClient.(*tests.MockLn)
	mock.SupportedNotificationTypes = &[]string{} // force the LookupInvoice path

	settledAt := time.Now().Unix()
	ln := &hashBoundLn{
		MockLn: mock,
		only:   ownHash,
		answer: &lnclient.Transaction{
			PaymentHash: ownHash,
			SettledAt:   &settledAt,
			Preimage:    "auditD-own-preimage",
			FeesPaid:    7,
		},
	}

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	require.NoError(t, transactionsService.checkUnsettledTransaction(context.Background(), &dbTransaction, ln))

	asked := ln.asksSoFar()
	require.Len(t, asked, 1, "reconciliation must do exactly one lookup")
	require.Equal(t, ownHash, asked[0],
		"the hub reconciled this transaction against hash %q, not its own %q — it would be "+
			"writing another invoice's preimage and fee onto this row", asked[0], ownHash)

	var settled db.Transaction
	require.NoError(t, svc.DB.First(&settled, dbTransaction.ID).Error)
	require.Equal(t, constants.TRANSACTION_STATE_SETTLED, settled.State)
	require.NotNil(t, settled.Preimage)
	require.Equal(t, "auditD-own-preimage", *settled.Preimage,
		"the settled row must carry ITS OWN invoice's preimage")
}

// TestAuditDQA_SweepStalePendingOutgoing_ReconcilesAgainstItsOwnHash is the same
// binding on the unattended path, which is the one that can force-FAIL a payment.
func TestAuditDQA_SweepStalePendingOutgoing_ReconcilesAgainstItsOwnHash(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const ownHash = "d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2"

	staleTime := time.Now().Add(-72 * time.Hour)
	staleTx := db.Transaction{
		State:       constants.TRANSACTION_STATE_PENDING,
		Type:        constants.TRANSACTION_TYPE_OUTGOING,
		PaymentHash: ownHash,
		AmountMloki: 123000,
		CreatedAt:   staleTime,
	}
	require.NoError(t, svc.DB.Create(&staleTx).Error)
	require.NoError(t, svc.DB.Model(&staleTx).Update("created_at", staleTime).Error)

	mock := svc.LNClient.(*tests.MockLn)
	mock.SupportedNotificationTypes = &[]string{}

	settledAt := time.Now().Unix()
	ln := &hashBoundLn{
		MockLn: mock,
		only:   ownHash,
		answer: &lnclient.Transaction{
			PaymentHash: ownHash,
			SettledAt:   &settledAt,
			Preimage:    "auditD-sweep-preimage",
			FeesPaid:    3,
		},
	}

	NewTransactionsService(svc.DB, svc.EventPublisher).
		SweepStalePendingOutgoing(context.Background(), ln)

	asked := ln.asksSoFar()
	require.Len(t, asked, 1)
	require.Equal(t, ownHash, asked[0],
		"the stale-pending sweep reconciled against %q, not this transaction's own hash %q", asked[0], ownHash)

	var refreshed db.Transaction
	require.NoError(t, svc.DB.First(&refreshed, staleTx.ID).Error)
	require.Equal(t, constants.TRANSACTION_STATE_SETTLED, refreshed.State,
		"a stale payment the node reports as settled must be recorded settled, not force-FAILED")
}
