package transactions

// F2 — Transfer API passes nil metadata to SendPaymentSync, so the JIT
// full-drain guard sees isInternalTransfer=false and rejects any partial drain
// from a JIT wallet even when the payment originates from the hub's internal
// Transfer API.
//
// The fix is at the caller level (api/transactions.go): Transfer now passes
// map[string]interface{}{"internal_transfer": true} to SendPaymentSync.
// The SendPaymentSync function itself correctly rejects partial drains when
// metadata is nil — that guard is working as intended.
//
// TestSendPaymentSync_JITWallet_NilMetadata_PartialDrain_IsBlocked verifies
// the guard works (nil metadata → rejected).
// TestSendPaymentSync_JITWallet_ExplicitInternalTransfer_PartialDrain_Succeeds
// verifies the fixed path (explicit flag → accepted).

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestSendPaymentSync_JITWallet_NilMetadata_PartialDrain_IsBlocked(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Create a parent hub app so parent_kind is populated on the child wallet.
	hub := &db.App{Name: "hub", Kind: db.AppKindJITHub}
	require.NoError(t, svc.DB.Create(hub).Error)

	// Create a JIT wallet child app with PAY_INVOICE scope.
	wallet := &db.App{
		Name:        "jit-wallet",
		Kind:        db.AppKindJITWallet,
		ParentAppID: &hub.ID,
		ParentKind:  db.ParentKindJIT,
	}
	require.NoError(t, svc.DB.Create(wallet).Error)

	perm := &db.AppPermission{
		AppId: wallet.ID,
		App:   *wallet,
		Scope: constants.PAY_INVOICE_SCOPE,
	}
	require.NoError(t, svc.DB.Create(perm).Error)

	// Fund the wallet with 500,000 mloki settled balance.
	const walletBalance uint64 = 500_000
	income := db.Transaction{
		AppId:       &wallet.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: walletBalance,
	}
	require.NoError(t, svc.DB.Create(&income).Error)

	// The mock LN always returns the same invoice (123,000 mloki).  This amount is
	// well below walletBalance, so it's a partial drain.
	// The Transfer API passes metadata=nil, reproducing the bug (F2).
	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	// metadata=nil mirrors what api.Transfer passes to SendPaymentSync.
	_, err = transactionsService.SendPaymentSync(
		tests.MockInvoice, nil, nil, svc.LNClient, &wallet.ID, &dbRequestEvent.ID,
	)

	// Nil metadata correctly triggers the partial-drain guard — this is expected.
	// The fix (F2) is that api.Transfer now passes {"internal_transfer": true},
	// not that nil metadata should bypass the guard.
	assert.Error(t, err, "nil metadata must be blocked by the JIT partial-drain guard; "+
		"the fix is the Transfer API caller passing the flag, not bypassing the guard for nil")
}

// TestSendPaymentSync_JITWallet_ExplicitInternalTransfer_PartialDrain_Succeeds
// shows the *current* workaround: passing metadata with "internal_transfer"=true.
// When F2 is fixed (Transfer passes the flag), this behaviour must still hold.
func TestSendPaymentSync_JITWallet_ExplicitInternalTransfer_PartialDrain_Succeeds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := &db.App{Name: "hub", Kind: db.AppKindJITHub}
	require.NoError(t, svc.DB.Create(hub).Error)

	wallet := &db.App{
		Name:        "jit-wallet",
		Kind:        db.AppKindJITWallet,
		ParentAppID: &hub.ID,
		ParentKind:  db.ParentKindJIT,
	}
	require.NoError(t, svc.DB.Create(wallet).Error)

	perm := &db.AppPermission{
		AppId: wallet.ID,
		App:   *wallet,
		Scope: constants.PAY_INVOICE_SCOPE,
	}
	require.NoError(t, svc.DB.Create(perm).Error)

	income := db.Transaction{
		AppId:       &wallet.ID,
		State:       constants.TRANSACTION_STATE_SETTLED,
		Type:        constants.TRANSACTION_TYPE_INCOMING,
		AmountMloki: 500_000,
	}
	require.NoError(t, svc.DB.Create(&income).Error)

	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)

	transactionsService := NewTransactionsService(svc.DB, svc.EventPublisher)
	// Passing the flag explicitly (the fix that Transfer should apply).
	_, err = transactionsService.SendPaymentSync(
		tests.MockInvoice, nil,
		map[string]interface{}{"internal_transfer": true},
		svc.LNClient, &wallet.ID, &dbRequestEvent.ID,
	)

	assert.NoError(t, err, "explicit internal_transfer=true must bypass the JIT partial-drain guard")
}
