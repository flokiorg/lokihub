package transactions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// newHoldPreimageHashPair returns a (preimage, payment_hash) pair where
// payment_hash is sha256(preimage), as a settled/cancelled hold invoice
// requires.
func newHoldPreimageHashPair(t *testing.T) (preimage string, paymentHash string) {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(raw), hex.EncodeToString(sum[:])
}

// createIsolatedApp makes a sandboxed (isolated-kind) connection, the kind whose
// hold invoices the owner-scoping must sandbox from each other.
func createIsolatedApp(t *testing.T, svc *tests.TestService) *db.App {
	t.Helper()
	app, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	app.Kind = db.AppKindIsolated
	require.NoError(t, svc.DB.Save(app).Error)
	return app
}

func seedAcceptedHoldInvoice(t *testing.T, svc *tests.TestService, ownerAppID uint, paymentHash, preimage string) {
	t.Helper()
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:       &ownerAppID,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		State:       constants.TRANSACTION_STATE_ACCEPTED,
		PaymentHash: paymentHash,
		Preimage:    &preimage,
		AmountMloki: 1000,
		Hold:        true,
		// SelfPayment keeps the owner's settle/cancel from touching the LN
		// client, so the test turns only on the app_id scoping.
		SelfPayment: true,
	}).Error)
}

func holdInvoiceState(t *testing.T, svc *tests.TestService, paymentHash string) string {
	t.Helper()
	var row db.Transaction
	require.NoError(t, svc.DB.Where(&db.Transaction{
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		PaymentHash: paymentHash,
	}).Limit(1).Find(&row).Error)
	return row.State
}

// TestCancelHoldInvoice_AppScoping is the authz regression test for
// LOKIHUB-0022: an isolated app that only knows the PUBLIC payment hash of
// another isolated sub-wallet's ACCEPTED hold invoice must not be able to
// cancel it. The attacker gets not-found (the same answer as a missing
// invoice), the invoice is untouched, and the owner can still cancel its own.
func TestCancelHoldInvoice_AppScoping(t *testing.T) {
	ctx := context.TODO()

	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	owner := createIsolatedApp(t, svc)
	attacker := createIsolatedApp(t, svc)

	_, paymentHash := newHoldPreimageHashPair(t)
	seedAcceptedHoldInvoice(t, svc, owner.ID, paymentHash, "")

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)

	// Attacker, knowing only the public payment hash, cannot cancel it.
	err = transactionsService.CancelHoldInvoice(ctx, paymentHash, svc.LNClient, &attacker.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, NewNotFoundError(), "a non-owner cancel must be not-found")
	assert.Equal(t, constants.TRANSACTION_STATE_ACCEPTED, holdInvoiceState(t, svc, paymentHash),
		"the victim's hold invoice must still be ACCEPTED after the attacker's cancel")
	assert.Empty(t, svc.LNClient.(*tests.MockLn).CancelledInvoices,
		"the attacker's cancel must never reach the LN client")

	// The owner can still cancel its own.
	err = transactionsService.CancelHoldInvoice(ctx, paymentHash, svc.LNClient, &owner.ID)
	require.NoError(t, err)
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, holdInvoiceState(t, svc, paymentHash),
		"the owner's own cancel must still fail the invoice")
}

// TestSettleHoldInvoice_AppScoping is the settle-path counterpart: an isolated
// app cannot settle another isolated sub-wallet's ACCEPTED hold invoice; the
// owner still can.
func TestSettleHoldInvoice_AppScoping(t *testing.T) {
	ctx := context.TODO()

	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	owner := createIsolatedApp(t, svc)
	attacker := createIsolatedApp(t, svc)

	preimage, paymentHash := newHoldPreimageHashPair(t)
	seedAcceptedHoldInvoice(t, svc, owner.ID, paymentHash, preimage)

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)

	// Attacker, even holding the preimage, cannot settle another app's invoice.
	_, err = transactionsService.SettleHoldInvoice(ctx, preimage, svc.LNClient, &attacker.ID)
	require.Error(t, err, "a non-owner settle must fail")
	assert.Equal(t, constants.TRANSACTION_STATE_ACCEPTED, holdInvoiceState(t, svc, paymentHash),
		"the victim's hold invoice must still be ACCEPTED after the attacker's settle")

	// The owner can still settle its own.
	settled, err := transactionsService.SettleHoldInvoice(ctx, preimage, svc.LNClient, &owner.ID)
	require.NoError(t, err)
	require.NotNil(t, settled)
	assert.Equal(t, constants.TRANSACTION_STATE_SETTLED, settled.State,
		"the owner's own settle must still settle the invoice")
}

// TestCancelHoldInvoice_StandardAppNotOverScoped guards the other half of the
// semantics: a standard (shared-wallet) connection is NOT isolated, so the
// owner-scoping must not newly restrict it — it keeps the pre-fix shared-wallet
// behavior, mirroring LookupTransaction/ListTransactions.
func TestCancelHoldInvoice_StandardAppNotOverScoped(t *testing.T) {
	ctx := context.TODO()

	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// A standard connection owns the hold invoice; a different standard
	// connection cancels it. Standard apps share the node wallet (no sandbox),
	// so this is allowed exactly as it was before the fix.
	owner, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	other, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	require.Equal(t, db.AppKindStandard, owner.Kind)

	_, paymentHash := newHoldPreimageHashPair(t)
	seedAcceptedHoldInvoice(t, svc, owner.ID, paymentHash, "")

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)

	err = transactionsService.CancelHoldInvoice(ctx, paymentHash, svc.LNClient, &other.ID)
	require.NoError(t, err, "a standard connection must not be over-restricted by the fix")
	assert.Equal(t, constants.TRANSACTION_STATE_FAILED, holdInvoiceState(t, svc, paymentHash))
}
