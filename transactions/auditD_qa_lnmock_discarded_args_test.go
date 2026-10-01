package transactions

// AUDIT-D QA / D-QA-3 — the closing tests for "the LN mock still discards the
// arguments of three money-moving calls".
//
// tests.MockLn now records SendPaymentSync's arguments and can honour
// MakeInvoice's amount. Three sibling calls were left behind, and each one
// accepts arguments it throws away:
//
//	SendKeysend(amount, destination, records, preimage) -> returns Fee: 1
//	MakeHoldInvoice(ctx, amount, ..., paymentHash)      -> returns a constant 2000-mloki tx
//	SettleHoldInvoice(ctx, preimage)                    -> returns nil
//
// Every existing assertion about these paths is made against the db.Transaction
// row the hub itself wrote from its own locals, so the row and the call cannot
// disagree inside a test. Demonstrated with three mutations that all compile
// (`go vet ./transactions/` clean) and all leave the suite green:
//
//	:724  SendKeysend(amount, destination, customRecords, preimage)
//	   -> SendKeysend(1, "02deadbeef...", customRecords, "0000...0000")
//	:335  MakeHoldInvoice(ctx, int64(amount), ..., paymentHash)
//	   -> MakeHoldInvoice(ctx, 1, ..., "0000...0000")
//	:1762 SettleHoldInvoice(ctx, preimage)
//	   -> SettleHoldInvoice(ctx, "0000...0000")
//
// These tests assert what the node was ASKED to do, which is the only thing a
// counterparty's money depends on.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// recordingLn embeds *tests.MockLn and overrides only the three calls whose
// arguments the shared mock drops, so it still satisfies lnclient.LNClient in
// full and the tracked fixture is not modified.
type recordingLn struct {
	*tests.MockLn

	mu sync.Mutex

	keysendAmounts      []uint64
	keysendDestinations []string
	keysendPreimages    []string

	holdAmounts []int64
	holdHashes  []string

	settlePreimages []string
}

func (ln *recordingLn) SendKeysend(amount uint64, destination string, customRecords []lnclient.TLVRecord, preimage string) (*lnclient.PayKeysendResponse, error) {
	ln.mu.Lock()
	ln.keysendAmounts = append(ln.keysendAmounts, amount)
	ln.keysendDestinations = append(ln.keysendDestinations, destination)
	ln.keysendPreimages = append(ln.keysendPreimages, preimage)
	ln.mu.Unlock()
	return &lnclient.PayKeysendResponse{Fee: 1}, nil
}

func (ln *recordingLn) MakeHoldInvoice(ctx context.Context, amount int64, description string, descriptionHash string, expiry int64, paymentHash string) (*lnclient.Transaction, error) {
	ln.mu.Lock()
	ln.holdAmounts = append(ln.holdAmounts, amount)
	ln.holdHashes = append(ln.holdHashes, paymentHash)
	ln.mu.Unlock()
	// A real node issues the invoice it was asked for: this amount, under this
	// hash. The shared mock returns a 2000-mloki constant under a constant hash
	// whatever it was asked for, which is why the DB row cannot disagree with it.
	tx := *tests.MockLNClientHoldTransaction
	tx.Amount = amount
	tx.PaymentHash = paymentHash
	return &tx, nil
}

func (ln *recordingLn) SettleHoldInvoice(ctx context.Context, preimage string) error {
	ln.mu.Lock()
	ln.settlePreimages = append(ln.settlePreimages, preimage)
	ln.mu.Unlock()
	return nil
}

func (ln *recordingLn) snapshot(f func()) {
	ln.mu.Lock()
	defer ln.mu.Unlock()
	f()
}

// TestAuditDQA_SendKeysend_AsksTheNodeForWhatTheLedgerRecorded pins all three of
// the arguments that decide where a keysend's money goes.
func TestAuditDQA_SendKeysend_AsksTheNodeForWhatTheLedgerRecorded(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	ln := &recordingLn{MockLn: svc.LNClient.(*tests.MockLn)}

	const amount = uint64(1000)
	const destination = "02a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendKeysend(amount, destination, nil, "", ln, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, transaction.Preimage)

	ln.snapshot(func() {
		require.Len(t, ln.keysendAmounts, 1, "exactly one keysend must have reached the node")
		require.Equalf(t, amount, ln.keysendAmounts[0],
			"the ledger debited %d mloki but the node was asked to send %d", transaction.AmountMloki, ln.keysendAmounts[0])
		require.Equalf(t, destination, ln.keysendDestinations[0],
			"the ledger recorded destination %q but the node was asked to pay %q — money to the wrong node",
			destination, ln.keysendDestinations[0])
		require.Equalf(t, *transaction.Preimage, ln.keysendPreimages[0],
			"the ledger stored preimage %q, whose sha256 is this transaction's payment hash, but the node "+
				"was handed %q — the recorded hash would never match the payment actually made",
			*transaction.Preimage, ln.keysendPreimages[0])
	})

	// And the row the ledger kept agrees with it, which is what the existing
	// tests check on their own.
	require.Equal(t, amount, transaction.AmountMloki)
}

// TestAuditDQA_MakeHoldInvoice_AsksTheNodeForTheRequestedAmountAndHash — a hold
// invoice issued under a hash the caller did not ask for can never be settled by
// that caller's preimage, so the payer's funds sit in an HTLC until it expires.
func TestAuditDQA_MakeHoldInvoice_AsksTheNodeForTheRequestedAmountAndHash(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	ln := &recordingLn{MockLn: svc.LNClient.(*tests.MockLn)}

	const amount = uint64(7777)
	const wantHash = "d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3"

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.MakeHoldInvoice(
		context.Background(), amount, "auditD hold", "", 0, wantHash, nil, ln, nil, nil)
	require.NoError(t, err)

	ln.snapshot(func() {
		require.Len(t, ln.holdAmounts, 1)
		require.EqualValues(t, amount, ln.holdAmounts[0],
			"the caller asked for a %d-mloki hold invoice; the node was asked for %d", amount, ln.holdAmounts[0])
		require.Equalf(t, wantHash, ln.holdHashes[0],
			"the caller asked to hold payment hash %q; the node was asked for %q — the payer's HTLC would "+
				"be locked to a hash the caller holds no preimage for", wantHash, ln.holdHashes[0])
	})

	require.EqualValues(t, amount, transaction.AmountMloki,
		"the ledger row must record the amount actually invoiced, not a fixture constant")
	require.Equal(t, wantHash, transaction.PaymentHash)
	require.True(t, transaction.Hold)
}

// TestAuditDQA_SettleHoldInvoice_HandsTheNodeThePreimageThatFoundTheRow covers
// the NON-self-payment branch, which the package's only hold tests skip entirely
// (both are self-payments, so `if !dbTransaction.SelfPayment` is false and the
// node is never called).
func TestAuditDQA_SettleHoldInvoice_HandsTheNodeThePreimageThatFoundTheRow(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	ln := &recordingLn{MockLn: svc.LNClient.(*tests.MockLn)}

	preimageBytes := make([]byte, 32)
	for i := range preimageBytes {
		preimageBytes[i] = 0xd4
	}
	preimage := hex.EncodeToString(preimageBytes)
	sum := sha256.Sum256(preimageBytes)
	paymentHash := hex.EncodeToString(sum[:])

	require.NoError(t, svc.DB.Create(&db.Transaction{
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_ACCEPTED,
		PaymentHash: paymentHash,
		AmountMloki: 4242,
		Hold:        true,
		SelfPayment: false,
	}).Error)

	settled, err := NewTransactionsService(svc.DB, svc.EventPublisher).
		SettleHoldInvoice(context.Background(), preimage, ln)
	require.NoError(t, err)
	require.Equal(t, constants.TRANSACTION_STATE_SETTLED, settled.State)

	ln.snapshot(func() {
		require.Len(t, ln.settlePreimages, 1, "the node must be told to settle exactly once")
		require.Equalf(t, preimage, ln.settlePreimages[0],
			"the row was located by sha256(%q) but the node was handed %q to settle with — the hub would "+
				"record a settlement the node never made, and the payer's HTLC would expire back to them",
			preimage, ln.settlePreimages[0])
	})
}
