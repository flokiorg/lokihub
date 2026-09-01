package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// The cash_consolidate fund-movement path (multi-source claim, N internal
// transfers, compensating rollback, nested-encrypted delivery) is verified in
// the integration suite, where a real node issues the distinct invoices each
// internal transfer decodes. These unit tests cover the request-validation and
// custody/same-hub guards, which fire before any funds move.

func handleCashConsolidateFor(t *testing.T, svc *tests.TestService, controller *nip47Controller, app *db.App, params cashConsolidateParams) *models.Response {
	t.Helper()
	content := map[string]interface{}{
		"method": constants.NIP47MethodCashConsolidate,
		"params": params,
	}
	reqBytes, _ := json.Marshal(content)
	nip47Request := &models.Request{}
	_ = json.Unmarshal(reqBytes, nip47Request)

	dbRequestEvent := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(dbRequestEvent).Error)

	var response *models.Response
	controller.HandleCashConsolidateEvent(context.TODO(), nip47Request, dbRequestEvent.ID, app, func(r *models.Response, _ nostr.Tags) {
		response = r
	}, nostr.Tags{})
	return response
}

func TestConsolidate_RequiresCashWalletApp(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// Dispatched against the hub itself (not a cash_wallet).
	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), hub, cashConsolidateParams{
		Sources:     []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPk},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)
}

func TestConsolidate_RequiresTwoSources(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), wallet, cashConsolidateParams{
		Sources:     []consolidateSourceParam{{WalletPubkey: *wallet.WalletPubkey}},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPk},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "at least two sources")
}

func TestConsolidate_NewIdentityMustBePubkey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), wallet, cashConsolidateParams{
		Sources:     []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: "deadbeef"},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "must be pubkey")
}

func TestConsolidate_RejectsConnectionKeySource(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), wallet, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *wallet.WalletPubkey, IdentityType: db.CashIdentityConnectionKey, IdentityValue: "abc", IdentityEvent: "{}"},
			{WalletPubkey: "b", IdentityType: db.CashIdentityConnectionKey, IdentityValue: "def", IdentityEvent: "{}"},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPk},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "connection_key sources are not supported")
}

// TestConsolidate_RejectsBearerSource is the regression for independent
// Security Auditor A's finding: a bearer source's secret has no signature and
// no binding to the request carrying it — unlike cash_transfer/cash_redeem
// (which never name a foreign wallet_pubkey, so a bearer secret only ever
// transits over its own single-recipient wallet's own connection),
// cash_consolidate lets a source name ANY wallet this node custodies. If a
// bearer source's secret were accepted here, it would sit in plaintext inside
// a request encrypted only under the CALLING connection's shared key — every
// co-recipient of a shared calling wallet, with no claim on that foreign
// bearer note, could decrypt it and race to steal the note. Rejected outright.
func TestConsolidate_RejectsBearerSource(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), wallet, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *wallet.WalletPubkey, BearerSecret: "00"},
			{WalletPubkey: "b", BearerSecret: "01"},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPk},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "bearer sources are not supported")
}

func TestConsolidate_SourceNotCustodied(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	newPk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// Sources whose wallet_pubkey this node does not custody. Custody is
	// checked before the identity fields are, so they can be left empty here.
	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), wallet, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: "deadbeef"},
			{WalletPubkey: "cafebabe"},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPk},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_NOT_FOUND, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "custodies")
}
