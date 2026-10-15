package cashwallet

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Minting Cash: each entry's identity_type MUST be pubkey,
// connection_key or cash (CASH-minting-cash-04). These are the wire values, so
// the constants are pinned to them.
func TestIdentityTypes_AreTheWireLiterals(t *testing.T) {
	assert.Equal(t, "pubkey", db.CashIdentityPubkey)
	assert.Equal(t, "connection_key", db.CashIdentityConnectionKey)
	assert.Equal(t, "cash", db.CashIdentityCash)
}

func mintRecipients(t *testing.T, recipients []RecipientInput) error {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")
	_, err = Create(context.TODO(), newTestDeps(svc), Params{HubApp: hub, Recipients: recipients, ExpirySecs: 1800})

	var bills int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Count(&bills).Error)
	if err != nil {
		assert.Zero(t, bills, "a refused mint creates no bill")
	}
	return err
}

// NIP-CASH §Minting Cash: amount_millis MUST be strictly positive
// (CASH-minting-cash-23).
func TestCreate_ZeroAmountRejected(t *testing.T) {
	err := mintRecipients(t, []RecipientInput{{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 0}})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

// NIP-CASH §Minting Cash: a request that fails a check MUST be rejected before
// step 7, the funding (CASH-minting-cash-32). A Hub without the balance refuses
// before it asks the node for a single invoice.
func TestCreate_InsufficientBalance_RefusedBeforeAnyInvoice(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 500, "fundtxhash") // less than the 1000 below
	_, err = Create(context.TODO(), newTestDeps(svc), Params{HubApp: hub, Recipients: oneCashRecipient(1000), ExpirySecs: 1800})
	require.Error(t, err)

	assert.Empty(t, svc.LNClient.(*tests.MockLn).MakeInvoiceAmounts(), "no invoice is made for a mint the Hub cannot fund")
	var bills int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Count(&bills).Error)
	assert.Zero(t, bills)
}

func TestCreate_UnknownIdentityTypeRejected(t *testing.T) {
	err := mintRecipients(t, []RecipientInput{{IdentityType: "email", IdentityValue: "a@example.com", AmountMloki: 1000}})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

// A cash slice's bill is handed to whoever holds the note, so it can never be
// shared with another recipient. Later code relies on this: a cash-mode
// split delivers its new bill's token in the clear because there is no
// co-holder to hide it from.
func TestCreate_CashRecipientAlongsideAnotherRejected(t *testing.T) {
	err := mintRecipients(t, []RecipientInput{
		{IdentityType: db.CashIdentityCash, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 1000},
	})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}
