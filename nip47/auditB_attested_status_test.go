package nip47

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipIC"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Scoping the Roster: "mine" comes from the item proof's own signer, and
// a caller whose proof verifies but holds no slice receives no rows — NOT_FOUND,
// never a wider roster. A pubkey holder's "mine" is its own row only. A
// connection_key slice's identity_value is a hash, never a signer, so a ck holder
// that presents NO attestation (nothing links its signer to the slice) is correctly
// NOT_FOUND here under every scope. The attested path by which a ck holder DOES read
// its row — presenting identity_value, ia_pubkey and attestation_event — is
// TestScopingTheRoster_ConnectionKeyAttested (LOKIHUB-0046).
func TestScopingTheRoster_SignerOnly(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	nip47svc.appsService = svc.AppsService
	nip47svc.identityAuthorityMgr = apps.NewIdentityAuthorityManager(svc.DB)

	hub := tests.CreateCashHub(t, svc, 200_000, 3600)
	iaPub := tests.RandomHex32()
	bobConnKey := tests.NewTestConnectionKey()
	bobPriv := nostr.GeneratePrivateKey()
	alicePriv := nostr.GeneratePrivateKey()
	alicePub, err := nostr.GetPublicKey(alicePriv)
	require.NoError(t, err)

	walletPubkey, connSecret, _ := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: alicePub, AmountMloki: 1000},
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: bobConnKey, IAPubkey: iaPub, AmountMloki: 7000},
	})

	hubXOnly := strings.Repeat("ab", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	ask := func(nonce, signerPriv, scope string) transport.Result {
		raw, err := json.Marshal(map[string]string{"scope": scope})
		require.NoError(t, err)
		item := buildItem(t, "i1", walletPubkey, constants.NIP47MethodCashStatus, string(raw),
			signerPriv, connSecret, hubXOnly, nonce, notAfter)
		res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
			PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
		require.True(t, served)
		return res
	}
	rows := func(r transport.Result) []nipcash.RecipientStatus {
		var out nipcash.CashStatusResult
		require.NoError(t, json.Unmarshal(r.Result, &out))
		return out.Recipients
	}

	// Bob's "mine" names no row.
	res := ask(strings.Repeat("a1", 32), bobPriv, "mine")
	require.NotNil(t, res.Error)
	require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)

	// Nor "all": no slice matches his signer, so no roster (no widening on no match).
	res = ask(strings.Repeat("a2", 32), bobPriv, "all")
	require.NotNil(t, res.Error)
	require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)

	// Alice reads "all": her signer matches a slice. Each connection_key row carries
	// its ia_pubkey (NIP-CASH §Cash Status).
	res = ask(strings.Repeat("a4", 32), alicePriv, "all")
	require.Nil(t, res.Error, "%+v", res.Error)
	all := rows(res)
	require.Len(t, all, 2)
	for _, r := range all {
		if r.IdentityType == db.CashIdentityConnectionKey {
			require.Equal(t, iaPub, r.IAPubkey)
		} else {
			require.Empty(t, r.IAPubkey)
		}
	}

	// Alice's "mine" is her own row only.
	res = ask(strings.Repeat("a3", 32), alicePriv, "mine")
	require.Nil(t, res.Error, "%+v", res.Error)
	mine := rows(res)
	require.Len(t, mine, 1)
	require.Equal(t, alicePub, mine[0].IdentityValue)
}

// TestScopingTheRoster_ConnectionKeyAttested closes LOKIHUB-0046: a connection_key
// recipient reads its own cash_status row by presenting identity_value, ia_pubkey
// and attestation_event, exactly as cash_withdraw_quote locates a ck slice
// (NIP-CASH §Quoting a Withdrawal applied to §Scoping the Roster). The Hub verifies
// the attestation (its p signs the item proof, d is the slice's identity_value,
// author is the ia_pubkey on the owner's allowlist) and scopes "mine" to that one
// row. A forged, wrong-holder, expired or non-matching attestation, or an untrusted
// IA, stays NOT_FOUND — never widening to the roster.
func TestScopingTheRoster_ConnectionKeyAttested(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	nip47svc.appsService = svc.AppsService
	nip47svc.identityAuthorityMgr = apps.NewIdentityAuthorityManager(svc.DB)

	hub := tests.CreateCashHub(t, svc, 200_000, 3600)

	// A trusted Identity Authority vouches for Bob's connection key.
	iaPriv := nostr.GeneratePrivateKey()
	iaPub, err := nostr.GetPublicKey(iaPriv)
	require.NoError(t, err)
	_, err = apps.NewIdentityAuthorityManager(svc.DB).Add(iaPub, "test-ia", nil)
	require.NoError(t, err)

	bobConnKey := tests.NewTestConnectionKey()
	bobPriv := nostr.GeneratePrivateKey()
	bobPub, err := nostr.GetPublicKey(bobPriv)
	require.NoError(t, err)
	alicePriv := nostr.GeneratePrivateKey()
	alicePub, err := nostr.GetPublicKey(alicePriv)
	require.NoError(t, err)

	walletPubkey, connSecret, _ := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: alicePub, AmountMloki: 1000},
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: bobConnKey, IAPubkey: iaPub, AmountMloki: 7000},
	})

	hubXOnly := strings.Repeat("ab", 32)
	notAfter := time.Now().Add(time.Minute).Unix()

	// attest builds a kind-35522 attestation by iaPrivKey binding connKey to claimant,
	// valid until exp.
	attest := func(iaPrivKey, connKey, claimant string, exp int64) string {
		ev := &nostr.Event{Kind: nipIC.KindAttestation, CreatedAt: nostr.Now(), Tags: nostr.Tags{
			{"d", connKey}, {"p", claimant}, {"platform", string(tests.TestPlatform)},
			{"evidence", tests.TestAttestationEvidence(connKey)}, {"expiration", strconv.FormatInt(exp, 10)},
		}}
		require.NoError(t, ev.Sign(iaPrivKey))
		b, err := json.Marshal(ev)
		require.NoError(t, err)
		return string(b)
	}

	ask := func(nonce, signerPriv string, params map[string]any) transport.Result {
		raw, err := json.Marshal(params)
		require.NoError(t, err)
		item := buildItem(t, "i1", walletPubkey, constants.NIP47MethodCashStatus, string(raw),
			signerPriv, connSecret, hubXOnly, nonce, notAfter)
		res, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
			PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
		require.True(t, served)
		return res
	}
	rows := func(r transport.Result) []nipcash.RecipientStatus {
		var out nipcash.CashStatusResult
		require.NoError(t, json.Unmarshal(r.Result, &out))
		return out.Recipients
	}
	good := time.Now().Add(24 * time.Hour).Unix()
	ck := func(claimant string, exp int64) map[string]any {
		return map[string]any{"identity_value": bobConnKey, "ia_pubkey": iaPub,
			"attestation_event": attest(iaPriv, bobConnKey, claimant, exp)}
	}

	// Scope absent (means "mine"): Bob's attestation resolves his ck row, his only row.
	res := ask(strings.Repeat("b1", 32), bobPriv, ck(bobPub, good))
	require.Nil(t, res.Error, "%+v", res.Error)
	mine := rows(res)
	require.Len(t, mine, 1, "a ck holder's mine is exactly its own row")
	require.Equal(t, db.CashIdentityConnectionKey, mine[0].IdentityType)
	require.Equal(t, bobConnKey, mine[0].IdentityValue)
	require.Equal(t, iaPub, mine[0].IAPubkey)
	require.EqualValues(t, 7000, mine[0].AmountMillis)

	// Explicit scope "mine" is identical.
	pMine := ck(bobPub, good)
	pMine["scope"] = "mine"
	res = ask(strings.Repeat("b2", 32), bobPriv, pMine)
	require.Nil(t, res.Error, "%+v", res.Error)
	require.Len(t, rows(res), 1)

	// Scope "all": Bob's attestation authorizes him, so the full roster comes back.
	pAll := ck(bobPub, good)
	pAll["scope"] = "all"
	res = ask(strings.Repeat("b3", 32), bobPriv, pAll)
	require.Nil(t, res.Error, "%+v", res.Error)
	require.Len(t, rows(res), 2, "an authorized ck holder reading scope=all sees the shared roster")

	// An attestation naming ANOTHER holder: its p is not Bob's signing key. NOT_FOUND.
	otherPriv := nostr.GeneratePrivateKey()
	otherPub, err := nostr.GetPublicKey(otherPriv)
	require.NoError(t, err)
	res = ask(strings.Repeat("b4", 32), bobPriv, ck(otherPub, good))
	require.NotNil(t, res.Error)
	require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)

	// A FORGED attestation (signed by a non-IA key, not the slice's ia_pubkey). NOT_FOUND.
	forgedIA := nostr.GeneratePrivateKey()
	forged := map[string]any{"identity_value": bobConnKey, "ia_pubkey": iaPub,
		"attestation_event": attest(forgedIA, bobConnKey, bobPub, good)}
	res = ask(strings.Repeat("b5", 32), bobPriv, forged)
	require.NotNil(t, res.Error)
	require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)

	// An EXPIRED attestation. NOT_FOUND.
	res = ask(strings.Repeat("b6", 32), bobPriv, ck(bobPub, time.Now().Add(-time.Hour).Unix()))
	require.NotNil(t, res.Error)
	require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)

	// A NON-MATCHING ck: identity_value belongs to no slice of this bill. NOT_FOUND.
	strangerConnKey := tests.NewTestConnectionKey()
	nonMatch := map[string]any{"identity_value": strangerConnKey, "ia_pubkey": iaPub,
		"attestation_event": attest(iaPriv, strangerConnKey, bobPub, good)}
	res = ask(strings.Repeat("b7", 32), bobPriv, nonMatch)
	require.NotNil(t, res.Error)
	require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)

	// Bob's "mine" with NO attestation at all stays NOT_FOUND (nothing links his signer
	// to the ck slice) — the disclosure guarantee the signer-only test also holds.
	res = ask(strings.Repeat("b8", 32), bobPriv, map[string]any{"scope": "mine"})
	require.NotNil(t, res.Error)
	require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)

	// The IA is removed from the owner's allowlist: a once-valid attestation no longer
	// resolves the row. NOT_FOUND, never RESTRICTED or a wider roster.
	require.NoError(t, apps.NewIdentityAuthorityManager(svc.DB).Delete(iaPub))
	res = ask(strings.Repeat("b9", 32), bobPriv, ck(bobPub, good))
	require.NotNil(t, res.Error)
	require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)

	// Re-trust the IA and confirm the row reads again — the bill was live throughout.
	_, err = apps.NewIdentityAuthorityManager(svc.DB).Add(iaPub, "test-ia", nil)
	require.NoError(t, err)
	res = ask(strings.Repeat("ba", 32), bobPriv, ck(bobPub, good))
	require.Nil(t, res.Error, "%+v", res.Error)
	require.Len(t, rows(res), 1)
}
