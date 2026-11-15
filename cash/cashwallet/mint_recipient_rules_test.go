package cashwallet

import (
	"context"
	"strings"
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

// NIP-CASH §Minting Cash: a request with one zero amount_millis beside a
// positive one is refused whole (CASH-50).
func TestCreate_ZeroAmountBesideAnotherRecipientRejected(t *testing.T) {
	err := mintRecipients(t, []RecipientInput{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 0},
	})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestCreate_OneMlokiBesideAnotherRecipientIsMinted(t *testing.T) {
	err := mintRecipients(t, []RecipientInput{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 1},
	})
	assert.NoError(t, err)
}

// NIP-CASH §Minting Cash (CASH-32): identity_value and ia_pubkey are 64
// lowercase hex; a slice stored under any other case could never be claimed.
func TestValidateIdentityShape_RequiresLowercaseHex(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	ia := tests.RandomHex32()
	registerTrustedIA(t, svc, ia)
	deps := newTestDeps(svc)
	value := tests.RandomHex32()
	mixed := value[:63] + "A"

	refused := []struct{ name, typ, value, ia string }{
		{"pubkey uppercase", db.CashIdentityPubkey, strings.ToUpper(value), ""},
		{"pubkey one uppercase digit", db.CashIdentityPubkey, mixed, ""},
		{"pubkey 63 digits", db.CashIdentityPubkey, value[:63], ""},
		{"pubkey 65 digits", db.CashIdentityPubkey, value + "0", ""},
		{"connection_key uppercase value", db.CashIdentityConnectionKey, strings.ToUpper(value), ia},
		{"connection_key uppercase ia_pubkey", db.CashIdentityConnectionKey, value, strings.ToUpper(ia)},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			assert.ErrorIs(t, ValidateIdentityShape(deps, c.typ, c.value, c.ia), constants.ErrInvalidParams)
		})
	}
	assert.NoError(t, ValidateIdentityShape(deps, db.CashIdentityPubkey, value, ""), "lowercase pubkey")
	assert.NoError(t, ValidateIdentityShape(deps, db.CashIdentityPubkey, strings.Repeat("0", 64), ""), "all-digit value")
	assert.NoError(t, ValidateIdentityShape(deps, db.CashIdentityPubkey, strings.Repeat("f", 64), ""), "all-f value")
	assert.NoError(t, ValidateIdentityShape(deps, db.CashIdentityConnectionKey, value, ia), "lowercase connection_key")
}

func TestCreate_UppercasePubkeyRecipientRejected(t *testing.T) {
	err := mintRecipients(t, []RecipientInput{{IdentityType: db.CashIdentityPubkey, IdentityValue: strings.ToUpper(tests.RandomHex32()), AmountMloki: 1000}})
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
	assert.NoError(t, mintRecipients(t, []RecipientInput{{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 1000}}))
}
