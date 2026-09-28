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
	"github.com/ohstr/nmilat/nipcash/transport"
)

// privateDispatchFixture builds a hub with one funded, single-recipient bill and returns
// what a client needs to address it: the bill's wallet pubkey and the recipient's key.
func privateDispatchFixture(t *testing.T, svc *tests.TestService) (walletPubkey, recipientPriv, recipientPub string) {
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
	return walletPubkey, recipientPriv, recipientPub
}

// buildItem assembles a real item with a real proof, the way the SDK does.
func buildItem(t *testing.T, id, target, method, params, signerPriv, hubXOnly, nonce string, notAfter int64) transport.Item {
	t.Helper()
	hash, err := transport.CanonicalParamsHash(json.RawMessage(params))
	require.NoError(t, err)
	proof, err := transport.BuildItemProof(signerPriv, transport.ProofBinding{
		Target: target, HubXOnly: hubXOnly, Method: method,
		ParamsHash: hash, Nonce: nonce, NotAfter: notAfter,
	})
	require.NoError(t, err)
	return transport.Item{
		ID: id, Target: target, Method: method,
		Params: json.RawMessage(params), Proof: proof,
	}
}

// TestServePrivateItem_ServesAnAuthorizedItem is the happy path: a real bill, a real proof
// signed by a real recipient, answered.
func TestServePrivateItem_ServesAnAuthorizedItem(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, _ := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	item := buildItem(t, "s1", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		recipientPriv, hubXOnly, nonce, notAfter)

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
	walletPubkey, recipientPriv, _ := privateDispatchFixture(t, svc)

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
				recipientPriv, hubXOnly, nonce, notAfter),
			why: "answering would confirm which bills exist",
		},
		{
			name: "a proof signed by someone who holds no slice",
			item: buildItem(t, "x2", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
				strangerPriv, hubXOnly, nonce, notAfter),
			why: "a verified proof is not the same as being a recipient",
		},
		{
			name: "a proof bound to a different hub",
			item: buildItem(t, "x3", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
				recipientPriv, strings.Repeat("99", 32), nonce, notAfter),
			why: "an item must not be replayable at another hub",
		},
		{
			name: "a proof bound to a different envelope",
			item: buildItem(t, "x4", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
				recipientPriv, hubXOnly, strings.Repeat("77", 32), notAfter),
			why: "a banked proof must not be replayable in a later envelope",
		},
		{
			name: "a method this transport does not serve",
			item: buildItem(t, "x5", walletPubkey, constants.NIP47MethodMintCash, `{}`,
				recipientPriv, hubXOnly, nonce, notAfter),
			why: "the allowlist itself must not be discoverable",
		},
		{
			name: "no proof on an identity-bound bill",
			item: transport.Item{ID: "x6", Target: walletPubkey,
				Method: constants.NIP47MethodCashStatus, Params: json.RawMessage(`{}`)},
			why: "a bill cannot dodge its proof by looking bearer",
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
	walletPubkey, recipientPriv, _ := privateDispatchFixture(t, svc)

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}

	for _, id := range []string{"a", "b", "c"} {
		item := buildItem(t, id, walletPubkey, constants.NIP47MethodCashStatus, `{}`,
			recipientPriv, hubXOnly, nonce, notAfter)
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
	walletPubkey, recipientPriv, _ := privateDispatchFixture(t, svc)

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
		recipientPriv, hubXOnly, nonce, notAfter)

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
