package nip47

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
)

// privateDispatchFixture builds a hub with one funded, single-recipient bill and returns
// what a client needs to address it: the bill's wallet pubkey and the recipient's key.
func privateDispatchFixture(t *testing.T, svc *tests.TestService) (walletPubkey, recipientPriv, recipientPub, connPriv string) {
	t.Helper()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtx")

	recipientPriv = nostr.GeneratePrivateKey()
	recipientPub, err := nostr.GetPublicKey(recipientPriv)
	require.NoError(t, err)

	// A bill is an app row plus one claim per recipient.
	wallet := db.App{
		Name: "bill-" + recipientPub[:8], Kind: db.AppKindCashWallet,
		ParentAppID: &hub.ID, ParentKind: db.ParentKindCash,
		AppPubkey: tests.RandomHex32(),
	}
	require.NoError(t, svc.DB.Create(&wallet).Error)

	walletKey, err := svc.Keys.GetAppWalletKey(wallet.ID)
	require.NoError(t, err)
	walletPubkey, err = nostr.GetPublicKey(walletKey)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&wallet).Update("wallet_pubkey", walletPubkey).Error)

	// app_pubkey must be the DETERMINISTIC pairing pubkey, exactly as
	// cashwallet.Create sets it — it is the pubkey of the connection secret inside
	// the bill's token, and therefore what a kind-23193 bill proof is checked
	// against.
	//
	// This fixture used to leave a random value here. Harmless while nothing checked
	// it; now it would make every bill proof fail, and the failure is an omission, so
	// every test would have gone quiet for a reason unrelated to what it asserts.
	connPriv, err = svc.Keys.GetCashPairingKey(wallet.ID)
	require.NoError(t, err)
	connPub, err := nostr.GetPublicKey(connPriv)
	require.NoError(t, err)
	require.NoError(t, svc.DB.Model(&wallet).Update("app_pubkey", connPub).Error)

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: recipientPub, AmountMloki: 1000},
	}))

	// The same scope set cashwallet.Create grants a real bill. Needed because the
	// dispatch checks permissions per item exactly as the standard path does — a bill
	// without them is correctly REFUSED rather than omitted, which is what the first
	// version of this fixture accidentally demonstrated.
	for _, scope := range []string{
		constants.CASH_REDEEM_SCOPE,
		constants.CASH_TRANSFER_SCOPE,
		constants.CASH_CONSOLIDATE_SCOPE,
		constants.GET_BALANCE_SCOPE,
	} {
		require.NoError(t, svc.DB.Create(&db.AppPermission{AppId: wallet.ID, Scope: scope}).Error)
	}
	return walletPubkey, recipientPriv, recipientPub, connPriv
}

// buildItem assembles a real item with both real proofs, the way the SDK does:
// a kind-23192 slice proof signed by the recipient, and a kind-23193 bill proof
// signed by the bill's connection key.
//
// Both are needed for the item to be served at all, and they are signed by
// DIFFERENT keys on purpose — that separation is the whole point of having two.
func buildItem(t *testing.T, id, target, method, params, signerPriv, connPriv, hubXOnly, nonce string, notAfter int64) transport.Item {
	t.Helper()
	hash, err := transport.CanonicalParamsHash(json.RawMessage(params))
	require.NoError(t, err)
	binding := transport.ProofBinding{
		Target: target, HubXOnly: hubXOnly, Method: method,
		ParamsHash: hash, Nonce: nonce, NotAfter: notAfter,
	}
	proof, err := transport.BuildItemProof(signerPriv, binding)
	require.NoError(t, err)
	item := transport.Item{
		ID: id, Target: target, Method: method,
		Params: json.RawMessage(params), Proof: proof,
	}
	if connPriv != "" {
		billProof, err := transport.BuildBillProof(connPriv, binding)
		require.NoError(t, err)
		item.BillProof = billProof
	}
	return item
}

// TestServePrivateItem_ServesAnAuthorizedItem is the happy path: a real bill, a real proof
// signed by a real recipient, answered.
func TestServePrivateItem_ServesAnAuthorizedItem(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	item := buildItem(t, "s1", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		recipientPriv, connPriv, hubXOnly, nonce, notAfter)

	result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	require.True(t, served, "an authorized item on a bill this hub holds must be served")
	assert.Equal(t, "s1", result.ID)
	assert.Nil(t, result.Error, "cash_status on a live bill must not error")
	assert.NotEmpty(t, result.Result, "a served cash_status must carry a roster")
}

// TestServePrivateItem_OmitsRatherThanErrors is the protocol rule this dispatch exists to
// honour, and the reason each case is a skip rather than an error result.
//
// A hub omits an item whose target it does not hold, whose proof does not verify, or whose
// method it does not serve — and those MUST be indistinguishable from outside. Telling them
// apart would turn batching into an oracle for which bills a hub holds: an attacker could
// enumerate bills by watching which items come back answered.
//
// So every case here asserts served=false, and specifically NOT an error result.
func TestServePrivateItem_OmitsRatherThanErrors(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	strangerPriv := nostr.GeneratePrivateKey()
	unknownTarget := strings.Repeat("ef", 32)

	for _, tc := range []struct {
		name string
		item transport.Item
		why  string
	}{
		{
			name: "a bill this hub does not hold",
			item: buildItem(t, "x1", unknownTarget, constants.NIP47MethodCashStatus, `{}`,
				recipientPriv, connPriv, hubXOnly, nonce, notAfter),
			why: "answering would confirm which bills exist",
		},
		{
			name: "a bill proof signed by the wrong key",
			item: buildItem(t, "x2", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
				recipientPriv, strangerPriv, hubXOnly, nonce, notAfter),
			why: "possession is what gates every answer; without it nothing may be confirmed",
		},
		{
			name: "no bill proof at all",
			item: func() transport.Item {
				it := buildItem(t, "x7", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
					recipientPriv, connPriv, hubXOnly, nonce, notAfter)
				it.BillProof = nil
				return it
			}(),
			why: "a missing bill proof must not fall through to an answer",
		},
		{
			name: "a slice proof presented as the bill proof",
			item: func() transport.Item {
				it := buildItem(t, "x8", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
					recipientPriv, connPriv, hubXOnly, nonce, notAfter)
				// Same six bindings, different kind. If the kind were not checked,
				// anyone able to sign anything could claim possession.
				it.BillProof = it.Proof
				return it
			}(),
			why: "the kind is the only thing separating a slice proof from a bill proof",
		},
		{
			name: "a proof bound to a different hub",
			item: buildItem(t, "x3", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
				recipientPriv, connPriv, strings.Repeat("99", 32), nonce, notAfter),
			why: "an item must not be replayable at another hub",
		},
		{
			name: "a proof bound to a different envelope",
			item: buildItem(t, "x4", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
				recipientPriv, connPriv, hubXOnly, strings.Repeat("77", 32), notAfter),
			why: "a banked proof must not be replayable in a later envelope",
		},
		{
			name: "a method this transport does not serve",
			item: buildItem(t, "x5", walletPubkey, constants.NIP47MethodMintCash, `{}`,
				recipientPriv, connPriv, hubXOnly, nonce, notAfter),
			why: "the allowlist itself must not be discoverable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, tc.item, binding)
			assert.False(t, served, "must be OMITTED, not answered: %s", tc.why)
			assert.Nil(t, result.Error,
				"an omitted item must carry no error — an error would distinguish it from a bill the hub does not hold")
		})
	}
}

// TestServePrivateItem_RecordsOneRequestEventPerItem: the controllers take a
// requestEventId, and the standard path creates one row per Nostr event. A batch is one
// event carrying N operations, so sharing a row would leave every audit record and failure
// log ambiguous about which operation it described.
func TestServePrivateItem_RecordsOneRequestEventPerItem(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	for _, id := range []string{"a", "b", "c"} {
		item := buildItem(t, id, walletPubkey, constants.NIP47MethodCashStatus, `{}`,
			recipientPriv, connPriv, hubXOnly, nonce, notAfter)
		_, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
		require.True(t, served, "item %s", id)
	}

	var rows []db.RequestEvent
	require.NoError(t, svc.DB.Where("nostr_id LIKE ?", nonce+":%").Find(&rows).Error)
	assert.Len(t, rows, 3, "each item needs its own request-event row")

	seen := map[string]bool{}
	for _, r := range rows {
		assert.False(t, seen[r.NostrId], "request-event ids must be distinct per item")
		seen[r.NostrId] = true
		assert.Equal(t, constants.NIP47MethodCashStatus, r.Method)
	}
}

// TestServePrivateItem_ARefusalIsAnAnswerNotAnOmission is the other half of the rule, and
// the distinction the whole three-state outcome design rests on.
//
// A hub that HOLDS the bill and declines must say so. Omitting instead would tell the caller
// nothing they can act on — and since an omission is never safe to resend, they would be
// stuck with an item they could neither diagnose nor retry.
//
// This case was found by accident: the first version of the fixture forgot to grant the
// bill's scopes, and the dispatch correctly answered RESTRICTED rather than omitting. Worth
// pinning deliberately rather than relying on that.
func TestServePrivateItem_ARefusalIsAnAnswerNotAnOmission(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, _, connPriv := privateDispatchFixture(t, svc)

	// Revoke the scope the bill needs, leaving everything else intact: this hub still
	// holds the bill and can identify the caller — it simply will not serve the method.
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?",
		walletAppIDFor(t, svc, walletPubkey), constants.CASH_REDEEM_SCOPE).
		Delete(&db.AppPermission{}).Error)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	item := buildItem(t, "r1", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		recipientPriv, connPriv, hubXOnly, nonce, notAfter)

	result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	require.True(t, served, "a refusal must be SERVED, so the caller learns the hub declined")
	require.NotNil(t, result.Error, "a refusal must carry a reason")
	assert.Equal(t, constants.ERROR_RESTRICTED, result.Error.Code)
	assert.Equal(t, "r1", result.ID)
}

func walletAppIDFor(t *testing.T, svc *tests.TestService, walletPubkey string) uint {
	t.Helper()
	var app db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", walletPubkey).First(&app).Error)
	return app.ID
}

// TestServePrivateItem_BadScopeIsAnErrorNotAnOmission covers the state cash_status'
// `scope` introduced, and it lands on the error side of the omit/error line for a
// specific reason.
//
// Reaching the controller at all means the proof verified and the signer holds a
// slice of this bill — so the caller has ALREADY established that the hub holds it.
// An error therefore discloses nothing an omission would have protected, and it is
// strictly more useful: a client that sent a malformed scope can be told so.
//
// Omitting instead would be the harmful choice here. An omission is
// information-free by design, so the client would see their item vanish and have no
// way to learn that the fault was their own — the exact trap the SDK's local scope
// check exists to keep them out of, and this is the other side of it.
func TestServePrivateItem_BadScopeIsAnErrorNotAnOmission(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, _, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	// Built raw on purpose: nipcash.StatusItem refuses this client-side, so the only
	// way a hub ever sees it is from a client that did not check.
	item := buildItem(t, "s1", walletPubkey, constants.NIP47MethodCashStatus,
		`{"scope":"everything"}`, recipientPriv, connPriv, hubXOnly, nonce, notAfter)

	result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	require.True(t, served, "a bad scope must be SERVED with an error: the caller already proved the hub holds this bill")
	require.NotNil(t, result.Error, "a bad scope must carry a reason the client can act on")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, result.Error.Code)
	assert.Equal(t, "s1", result.ID)
}

// TestServePrivateItem_UnscopedStatusReturnsOnlyTheCallersRow is the dispatch-level
// half of the scope default: absent means "mine" here, because a per-item proof
// makes the caller identifiable for the first time (NIP-CASH §Scoping the Roster).
//
// privateDispatchFixture's bill has one recipient, so this asserts the row's
// identity rather than the count — the two-recipient version of this lives in
// service/private_full_loop_test.go, where it can tell "mine" and "all" apart.
func TestServePrivateItem_UnscopedStatusReturnsOnlyTheCallersRow(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, recipientPub, connPriv := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	item := buildItem(t, "u1", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		recipientPriv, connPriv, hubXOnly, nonce, notAfter)

	result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	require.True(t, served)
	require.Nil(t, result.Error)

	var roster nipcash.CashStatusResult
	require.NoError(t, json.Unmarshal(result.Result, &roster))
	require.Len(t, roster.Recipients, 1)
	assert.Equal(t, recipientPub, roster.Recipients[0].IdentityValue,
		"the scoped row must be the proof signer's own")
}
