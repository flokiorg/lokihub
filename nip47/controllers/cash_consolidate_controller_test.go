package controllers

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
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

func TestConsolidate_NewIdentityMustBeRecognizedType(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), wallet, cashConsolidateParams{
		Sources:     []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: "carrier-pigeon", IdentityValue: "deadbeef"},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "must be")
	assert.Contains(t, resp.Error.Message, db.CashIdentityPubkey)
	assert.Contains(t, resp.Error.Message, db.CashIdentityConnectionKey)
	assert.Contains(t, resp.Error.Message, db.CashIdentityBearer)
}

// TestConsolidate_BearerTarget_MissingIdentityValue_Rejected,
// TestConsolidate_BearerTarget_MalformedIdentityValue_Rejected, and
// TestConsolidate_BearerTarget_IAPubkeySupplied_Rejected mirror
// cash_transfer's own three bearer-target validation tests exactly (same
// three rejections, new call site) — see cash_transfer_controller_test.go.

func TestConsolidate_BearerTarget_MissingIdentityValue_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), wallet, cashConsolidateParams{
		Sources:     []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "required for a bearer target")
}

func TestConsolidate_BearerTarget_MalformedIdentityValue_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), wallet, cashConsolidateParams{
		Sources:     []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: "not-hex-and-not-64-chars"},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "64-character lowercase")
}

func TestConsolidate_BearerTarget_IAPubkeySupplied_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	iaPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), wallet, cashConsolidateParams{
		Sources: []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
		NewIdentity: cashTransferNewIdentityParam{
			IdentityType: db.CashIdentityBearer, IdentityValue: tests.RandomHex32(), IAPubkey: iaPub,
		},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "must not carry ia_pubkey")
}

func TestConsolidate_ConnectionKeyTarget_UntrustedIA_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	untrustedIA, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), wallet, cashConsolidateParams{
		Sources: []consolidateSourceParam{{WalletPubkey: "a"}, {WalletPubkey: "b"}},
		NewIdentity: cashTransferNewIdentityParam{
			IdentityType: db.CashIdentityConnectionKey, IdentityValue: tests.RandomHex32(), IAPubkey: untrustedIA,
		},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "not a trusted Identity Authority")
}

// TestConsolidate_RejectsBearerSource is a regression test: a bearer source's secret has no signature and
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

// TestHandleCashConsolidateEvent_BearerTarget_HappyPath: sum of two pubkey
// sources lands in a new bearer-identified wallet, delivered in the clear
// (not nested-encrypted — a bearer commitment has no real pubkey to ECDH
// against), and the response never carries the raw secret the caller
// generated locally.
func TestHandleCashConsolidateEvent_BearerTarget_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)

	secretHex, secretHash, err := cashwallet.GenerateBearerSecret()
	require.NoError(t, err)

	// amount matches tests.MockInvoice's own fixed encoded bolt11 amount
	// (123,000 mloki) — MockLn's balance check decodes the real invoice
	// string, not the queue entry's Amount field, so this must match exactly
	// (same convention every other consolidate test using this mock follows,
	// e.g. TestConsolidate_StrandedSource_ClaimLeftInPlace's moved=123_000).
	const amount = uint64(123_000)
	s1 := guardSource(t, svc, hub, ownerPub, amount, 0, 0)
	tests.FundApp(svc, s1.ID, 200_000, "s1-fund")
	s2 := guardSource(t, svc, hub, ownerPub, 1_000, 0, 0)
	tests.FundApp(svc, s2.ID, 200_000, "s2-fund")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: int64(amount)},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, Preimage: "p2", Amount: int64(1_000)},
	}

	proof1 := buildTransferProofEvent(t, ownerPriv, *s1.WalletPubkey, db.CashIdentityBearer, secretHash, "", amount, nil, time.Now())
	proof2 := buildTransferProofEvent(t, ownerPriv, *s2.WalletPubkey, db.CashIdentityBearer, secretHash, "", 1_000, nil, time.Now())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *s1.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof1)},
			{WalletPubkey: *s2.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof2)},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityBearer, IdentityValue: secretHash},
	})
	require.Nil(t, resp.Error)
	result, ok := resp.Result.(cashConsolidateResponse)
	require.True(t, ok, "unexpected result type %T", resp.Result)

	assert.True(t, strings.HasPrefix(result.NewWalletToken, "lokicash1"),
		"bearer target must be delivered in the clear, not nested-encrypted: got %q", result.NewWalletToken)
	assert.NotContains(t, result.NewWalletToken, secretHex,
		"the response must never contain the raw secret the caller generated")

	var mergedApp db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", result.NewWalletPubkey).First(&mergedApp).Error)
	claim := cashWalletClaimByIdentity(t, svc, mergedApp.ID, db.CashIdentityBearer, secretHash)
	require.NotNil(t, claim)
	assert.EqualValues(t, amount+1_000, claim.AmountMloki)
}

// TestHandleCashConsolidateEvent_ConnectionKeyTarget_HappyPath: sum of two
// pubkey sources lands in a new connection_key-identified wallet, ia_pubkey
// validated against the live trusted-IA allowlist, delivered in the clear
// (a connection_key has no real pubkey to ECDH against yet either).
func TestHandleCashConsolidateEvent_ConnectionKeyTarget_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)

	iaPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	registerTrustedIA(t, svc, iaPub)
	newConnectionKey := tests.RandomHex32()

	// amount matches tests.MockInvoice's own fixed encoded bolt11 amount —
	// see the identical comment in the bearer-target test above.
	const amount = uint64(123_000)
	s1 := guardSource(t, svc, hub, ownerPub, amount, 0, 0)
	tests.FundApp(svc, s1.ID, 200_000, "s1-fund")
	s2 := guardSource(t, svc, hub, ownerPub, 1_000, 0, 0)
	tests.FundApp(svc, s2.ID, 200_000, "s2-fund")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: int64(amount)},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, Preimage: "p2", Amount: int64(1_000)},
	}

	proof1 := buildTransferProofEvent(t, ownerPriv, *s1.WalletPubkey, db.CashIdentityConnectionKey, newConnectionKey, iaPub, amount, nil, time.Now())
	proof2 := buildTransferProofEvent(t, ownerPriv, *s2.WalletPubkey, db.CashIdentityConnectionKey, newConnectionKey, iaPub, 1_000, nil, time.Now())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *s1.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof1)},
			{WalletPubkey: *s2.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof2)},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityConnectionKey, IdentityValue: newConnectionKey, IAPubkey: iaPub},
	})
	require.Nil(t, resp.Error)
	result, ok := resp.Result.(cashConsolidateResponse)
	require.True(t, ok, "unexpected result type %T", resp.Result)
	assert.True(t, strings.HasPrefix(result.NewWalletToken, "lokicash1"),
		"connection_key target must be delivered in the clear, not nested-encrypted: got %q", result.NewWalletToken)

	var mergedApp db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", result.NewWalletPubkey).First(&mergedApp).Error)
	claim := cashWalletClaimByIdentity(t, svc, mergedApp.ID, db.CashIdentityConnectionKey, newConnectionKey)
	require.NotNil(t, claim)
	assert.Equal(t, iaPub, claim.IAPubkey)
	assert.EqualValues(t, amount+1_000, claim.AmountMloki)
}

// TestHandleCashConsolidateEvent_ConnectionKeySource_HappyPath: a mixed batch
// (one pubkey source, one connection_key source, both merged into a pubkey
// target) — the doc's legend describes source types as "mutually exclusive"
// only as a description of today's pubkey-only behavior, not a hard rule, and
// this proves pubkey+connection_key mixing in one call is in fact accepted.
func TestHandleCashConsolidateEvent_ConnectionKeySource_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	// pubkey source — amount matches tests.MockInvoice's fixed encoded amount
	// (see the identical comment on the bearer-target happy path above).
	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	const pubkeyAmount = uint64(123_000)
	s1 := guardSource(t, svc, hub, ownerPub, pubkeyAmount, 0, 0)
	tests.FundApp(svc, s1.ID, 200_000, "s1-fund")

	// connection_key source
	iaPriv := nostr.GeneratePrivateKey()
	iaPub, _ := nostr.GetPublicKey(iaPriv)
	registerTrustedIA(t, svc, iaPub)
	connectionKey := tests.RandomHex32()
	claimantPriv := nostr.GeneratePrivateKey()
	claimantPub, _ := nostr.GetPublicKey(claimantPriv)
	s2 := guardCashWallet(t, svc, hub)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(s2.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: connectionKey, IAPubkey: iaPub, AmountMloki: 1_000},
	}))
	tests.FundApp(svc, s2.ID, 200_000, "s2-fund")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: int64(pubkeyAmount)},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, Preimage: "p2", Amount: int64(1_000)},
	}

	proof1 := buildTransferProofEvent(t, ownerPriv, *s1.WalletPubkey, db.CashIdentityPubkey, newPub, "", pubkeyAmount, nil, time.Now())
	attestation := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, oneHourFromNow())
	proof2 := buildTransferProofEvent(t, claimantPriv, *s2.WalletPubkey, db.CashIdentityPubkey, newPub, "", 1_000,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *s1.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof1)},
			{
				WalletPubkey: *s2.WalletPubkey, IdentityType: db.CashIdentityConnectionKey, IdentityValue: connectionKey,
				IdentityEvent: mustMarshal(t, proof2), AttestationEvent: mustMarshal(t, attestation),
			},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
	})
	require.Nil(t, resp.Error)
	result, ok := resp.Result.(cashConsolidateResponse)
	require.True(t, ok, "unexpected result type %T", resp.Result)
	assert.EqualValues(t, pubkeyAmount+1_000, result.AmountMillis)
}

// TestConsolidate_ConnectionKeySource_UntrustedIA_Rejected mirrors
// TestHandleCashTransferEvent_NewIdentityConnectionKey_UntrustedIA_Rejected's
// pattern, applied to a consolidate SOURCE's own IA instead of the target's.
func TestConsolidate_ConnectionKeySource_UntrustedIA_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	s1 := guardSource(t, svc, hub, ownerPub, 123_000, 0, 0)

	// untrustedIA is never registered — no registerTrustedIA call.
	untrustedIAPriv := nostr.GeneratePrivateKey()
	untrustedIAPub, _ := nostr.GetPublicKey(untrustedIAPriv)
	connectionKey := tests.RandomHex32()
	claimantPriv := nostr.GeneratePrivateKey()
	claimantPub, _ := nostr.GetPublicKey(claimantPriv)
	s2 := guardCashWallet(t, svc, hub)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(s2.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: connectionKey, IAPubkey: untrustedIAPub, AmountMloki: 1_000},
	}))

	proof1 := buildTransferProofEvent(t, ownerPriv, *s1.WalletPubkey, db.CashIdentityPubkey, newPub, "", 123_000, nil, time.Now())
	attestation := buildIAAttestationEvent(t, untrustedIAPriv, connectionKey, claimantPub, oneHourFromNow())
	proof2 := buildTransferProofEvent(t, claimantPriv, *s2.WalletPubkey, db.CashIdentityPubkey, newPub, "", 1_000,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *s1.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof1)},
			{
				WalletPubkey: *s2.WalletPubkey, IdentityType: db.CashIdentityConnectionKey, IdentityValue: connectionKey,
				IdentityEvent: mustMarshal(t, proof2), AttestationEvent: mustMarshal(t, attestation),
			},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "revoked")

	// Untouched — neither source may have moved.
	claim1 := cashWalletClaimByIdentity(t, svc, s1.ID, db.CashIdentityPubkey, ownerPub)
	require.NotNil(t, claim1)
	assert.Nil(t, claim1.ClaimedAt)
}

// TestConsolidate_ConnectionKeySource_AttestationForWrongClaimant_Rejected: the
// attestation is real and IA-trusted, but attests a DIFFERENT pubkey than the
// one that actually signed this source's identity_event — mirrors
// verifyClaimAttestationEvent's own "wrong claimant" check, exercised here at
// the consolidate call site rather than transfer/redeem's.
func TestConsolidate_ConnectionKeySource_AttestationForWrongClaimant_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	s1 := guardSource(t, svc, hub, ownerPub, 123_000, 0, 0)

	iaPriv := nostr.GeneratePrivateKey()
	iaPub, _ := nostr.GetPublicKey(iaPriv)
	registerTrustedIA(t, svc, iaPub)
	connectionKey := tests.RandomHex32()
	claimantPriv := nostr.GeneratePrivateKey()
	strangerPriv := nostr.GeneratePrivateKey()
	strangerPub, _ := nostr.GetPublicKey(strangerPriv)
	s2 := guardCashWallet(t, svc, hub)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(s2.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: connectionKey, IAPubkey: iaPub, AmountMloki: 1_000},
	}))

	proof1 := buildTransferProofEvent(t, ownerPriv, *s1.WalletPubkey, db.CashIdentityPubkey, newPub, "", 123_000, nil, time.Now())
	// Attestation vouches for strangerPub, but the identity_event below is
	// signed by claimantPriv — a mismatch verifyClaimAttestationEvent must
	// catch.
	attestation := buildIAAttestationEvent(t, iaPriv, connectionKey, strangerPub, oneHourFromNow())
	proof2 := buildTransferProofEvent(t, claimantPriv, *s2.WalletPubkey, db.CashIdentityPubkey, newPub, "", 1_000,
		nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *s1.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof1)},
			{
				WalletPubkey: *s2.WalletPubkey, IdentityType: db.CashIdentityConnectionKey, IdentityValue: connectionKey,
				IdentityEvent: mustMarshal(t, proof2), AttestationEvent: mustMarshal(t, attestation),
			},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
}

// countingIAChecker wraps a fixed trust map and counts how many times
// IsTrusted is actually invoked per pubkey — lets a test prove a trust
// snapshot was taken once per call, not re-queried live per source.
type countingIAChecker struct {
	mu    sync.Mutex
	calls map[string]int
	trust map[string]bool
}

func (c *countingIAChecker) IsTrusted(pubkey string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[pubkey]++
	return c.trust[pubkey], nil
}

// TestConsolidate_ConnectionKeySource_TrustCheckedOncePerCall_NotPerSource is
// the load-bearing regression for the TOCTOU fix in
// resolveConsolidateSource's iaTrustCache: two connection_key sources citing
// the SAME ia_pubkey in one batch must only invoke IsTrusted once, proving
// every source in a batch is evaluated against one consistent point-in-time
// trust snapshot rather than a fresh live query each — the property the
// design doc calls for explicitly, not just an implied side effect of the
// happy path.
func TestConsolidate_ConnectionKeySource_TrustCheckedOncePerCall_NotPerSource(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	caller := guardCashWallet(t, svc, hub)
	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	iaPriv := nostr.GeneratePrivateKey()
	iaPub, _ := nostr.GetPublicKey(iaPriv)
	checker := &countingIAChecker{calls: map[string]int{}, trust: map[string]bool{iaPub: true}}

	permissionsSvc := permissions.NewPermissionsService(svc.DB, svc.EventPublisher)
	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	controller := NewNip47Controller(svc.LNClient, svc.DB, svc.EventPublisher, permissionsSvc, transactionsSvc,
		svc.AppsService, svc.Keys, nil, NewRateLimiter(), NewRateLimiter(), NewRateLimiter(), svc.Cfg, checker)

	connKey1 := tests.RandomHex32()
	connKey2 := tests.RandomHex32()
	claimant1Priv := nostr.GeneratePrivateKey()
	claimant1Pub, _ := nostr.GetPublicKey(claimant1Priv)
	claimant2Priv := nostr.GeneratePrivateKey()
	claimant2Pub, _ := nostr.GetPublicKey(claimant2Priv)

	// s1's amount matches tests.MockInvoice's fixed encoded amount, s2's
	// matches tests.MockLNClientHoldTransaction's — see the identical comment
	// on the bearer-target happy path above for why arbitrary amounts don't
	// work against these canned mock invoices.
	const s1Amount, s2Amount = uint64(123_000), uint64(1_000)
	s1 := guardCashWallet(t, svc, hub)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(s1.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: connKey1, IAPubkey: iaPub, AmountMloki: int64(s1Amount)},
	}))
	tests.FundApp(svc, s1.ID, 200_000, "s1-fund")
	s2 := guardCashWallet(t, svc, hub)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(s2.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: connKey2, IAPubkey: iaPub, AmountMloki: int64(s2Amount)},
	}))
	tests.FundApp(svc, s2.ID, 200_000, "s2-fund")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, Preimage: "p1", Amount: int64(s1Amount)},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, Preimage: "p2", Amount: int64(s2Amount)},
	}

	attestation1 := buildIAAttestationEvent(t, iaPriv, connKey1, claimant1Pub, oneHourFromNow())
	proof1 := buildTransferProofEvent(t, claimant1Priv, *s1.WalletPubkey, db.CashIdentityPubkey, newPub, "", s1Amount,
		nostr.Tags{{"connection_key", connKey1}, {"e", attestation1.ID}}, time.Now())
	attestation2 := buildIAAttestationEvent(t, iaPriv, connKey2, claimant2Pub, oneHourFromNow())
	proof2 := buildTransferProofEvent(t, claimant2Priv, *s2.WalletPubkey, db.CashIdentityPubkey, newPub, "", s2Amount,
		nostr.Tags{{"connection_key", connKey2}, {"e", attestation2.ID}}, time.Now())

	resp := handleCashConsolidateFor(t, svc, controller, caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{
				WalletPubkey: *s1.WalletPubkey, IdentityType: db.CashIdentityConnectionKey, IdentityValue: connKey1,
				IdentityEvent: mustMarshal(t, proof1), AttestationEvent: mustMarshal(t, attestation1),
			},
			{
				WalletPubkey: *s2.WalletPubkey, IdentityType: db.CashIdentityConnectionKey, IdentityValue: connKey2,
				IdentityEvent: mustMarshal(t, proof2), AttestationEvent: mustMarshal(t, attestation2),
			},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
	})
	require.Nil(t, resp.Error)
	assert.Equal(t, 1, checker.calls[iaPub],
		"both sources cite the same IA — IsTrusted must be invoked once per call (snapshot), not once per source")
}
