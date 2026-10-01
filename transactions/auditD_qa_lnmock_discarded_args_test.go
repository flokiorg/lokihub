package transactions

// AUDIT-D QA / D-QA-3 — the closing tests for "the LN mock still discards the
// arguments of three money-moving calls".
//
// When these were written, tests.MockLn recorded SendPaymentSync's arguments and
// could honour MakeInvoice's amount, while three sibling calls were left behind,
// each accepting arguments it threw away:
//
//	SendKeysend(amount, destination, records, preimage) -> returns Fee: 1
//	MakeHoldInvoice(ctx, amount, ..., paymentHash)      -> returns a constant 2000-mloki tx
//	SettleHoldInvoice(ctx, preimage)                    -> returns nil
//
// FIXED 2026-10-01: the shared mock now records all three and issues the hold
// invoice it was ASKED for, so these tests no longer need a local wrapper and the
// whole suite is a detector rather than just this file. That change is what matters
// — a wrapper here only protected the three paths below, and the mutations were
// still invisible to every other test in the repository. It immediately paid for
// itself: TestHandleMakeHoldInvoiceEvent turned out to assert that make_hold_invoice
// returns a DIFFERENT payment hash than the caller requested, which passed only
// because the mock substituted its own constant.
//
// These tests are kept, converted onto the shared recordings, because they assert
// things no other test does — notably the non-self-payment settle branch below.
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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestAuditDQA_SendKeysend_AsksTheNodeForWhatTheLedgerRecorded pins all three of
// the arguments that decide where a keysend's money goes.
func TestAuditDQA_SendKeysend_AsksTheNodeForWhatTheLedgerRecorded(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	ln := svc.LNClient.(*tests.MockLn)

	const amount = uint64(1000)
	const destination = "02a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.SendKeysend(amount, destination, nil, "", ln, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, transaction.Preimage)

	calls := ln.SendKeysendCalls()
	require.Len(t, calls, 1, "exactly one keysend must have reached the node")
	require.Equalf(t, amount, calls[0].Amount,
		"the ledger debited %d mloki but the node was asked to send %d", transaction.AmountMloki, calls[0].Amount)
	require.Equalf(t, destination, calls[0].Destination,
		"the ledger recorded destination %q but the node was asked to pay %q — money to the wrong node",
		destination, calls[0].Destination)
	require.Equalf(t, *transaction.Preimage, calls[0].Preimage,
		"the ledger stored preimage %q, whose sha256 is this transaction's payment hash, but the node "+
			"was handed %q — the recorded hash would never match the payment actually made",
		*transaction.Preimage, calls[0].Preimage)

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

	ln := svc.LNClient.(*tests.MockLn)

	const amount = uint64(7777)
	const wantHash = "d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3"

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	transaction, err := transactionsService.MakeHoldInvoice(
		context.Background(), amount, "auditD hold", "", 0, wantHash, nil, ln, nil, nil)
	require.NoError(t, err)

	holdCalls := ln.MakeHoldInvoiceCalls()
	require.Len(t, holdCalls, 1)
	require.EqualValues(t, amount, holdCalls[0].Amount,
		"the caller asked for a %d-mloki hold invoice; the node was asked for %d", amount, holdCalls[0].Amount)
	require.Equalf(t, wantHash, holdCalls[0].PaymentHash,
		"the caller asked to hold payment hash %q; the node was asked for %q — the payer's HTLC would "+
			"be locked to a hash the caller holds no preimage for", wantHash, holdCalls[0].PaymentHash)

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

	ln := svc.LNClient.(*tests.MockLn)

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

	settles := ln.SettleHoldPreimages()
	require.Len(t, settles, 1, "the node must be told to settle exactly once")
	require.Equalf(t, preimage, settles[0],
		"the row was located by sha256(%q) but the node was handed %q to settle with — the hub would "+
			"record a settlement the node never made, and the payer's HTLC would expire back to them",
		preimage, settles[0])
}
