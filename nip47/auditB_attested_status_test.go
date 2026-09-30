package nip47

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// A connection_key recipient could not read their own row after the roster-disclosure
// fix, and this is the restoration.
//
// Their identity_value is hex(sha256(platform + ":" + externalID)) — never a pubkey —
// so the item's signer can never equal it and the transport gate has nothing to
// compare. Before the fix the gate ABSTAINED in that case, which is what let any token
// holder read the whole roster. Closing that took this mode's only read with it.
//
// cash_redeem already solves the same problem with an IA attestation binding the
// claimant's keypair to that identity. cash_status now accepts the same evidence, and
// the Hub resolves WHICH claim from the attestation itself rather than from anything
// the caller names — pointing at someone else's row cannot work, because the
// attestation would not cover it.
//
// Four cases: the restoration, and three ways it must not become a new hole.
func TestAuditB_AttestedConnectionKeyReadsOnlyItsOwnRow(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// The real constructor, not a hand-filled struct: the authorized path dispatches
	// into a controller, so a minimal service panics once an item actually passes the
	// gate — which is exactly what the positive case below is testing.
	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	nip47svc.appsService = svc.AppsService
	nip47svc.identityAuthorityMgr = apps.NewIdentityAuthorityManager(svc.DB)

	hub := tests.CreateCashHub(t, svc, 200_000, 3600)

	// The IA, registered and trusted.
	iaPriv := nostr.GeneratePrivateKey()
	iaPub, err := nostr.GetPublicKey(iaPriv)
	require.NoError(t, err)
	iaMgr := apps.NewIdentityAuthorityManager(svc.DB)
	_, err = iaMgr.Add(iaPub, "test IA", []string{"wss://relay.test"})
	require.NoError(t, err)

	// Bob's connection_key identity, and the keypair the IA will attest for it.
	sum := sha256.Sum256([]byte("platform:bob-external-id"))
	bobConnKey := hex.EncodeToString(sum[:])
	bobPriv := nostr.GeneratePrivateKey()
	bobPub, err := nostr.GetPublicKey(bobPriv)
	require.NoError(t, err)

	alicePriv := nostr.GeneratePrivateKey()
	alicePub, err := nostr.GetPublicKey(alicePriv)
	require.NoError(t, err)

	walletPubkey, connSecret, _ := auditBBill(t, svc, hub.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: alicePub, AmountMloki: 1000},
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: bobConnKey,
			IAPubkey: iaPub, AmountMloki: 7000},
	})

	hubXOnly := strings.Repeat("ab", 32)
	notAfter := time.Now().Add(time.Minute).Unix()

	ask := func(nonce, signerPriv, attestationJSON string) (transport.Result, bool) {
		params := nipcash.CashStatusRequest{Scope: "mine", AttestationEvent: attestationJSON}
		raw, mErr := json.Marshal(params)
		require.NoError(t, mErr)
		binding := PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter}
		item := buildItem(t, "i1", walletPubkey, constants.NIP47MethodCashStatus, string(raw),
			signerPriv, connSecret, hubXOnly, nonce, notAfter)
		return nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item, binding)
	}
	rosterOf := func(r transport.Result) []nipcash.RecipientStatus {
		var out nipcash.CashStatusResult
		require.NoError(t, json.Unmarshal(r.Result, &out))
		return out.Recipients
	}

	attestation := buildIAAttestationEventFor(t, iaPriv, bobConnKey, bobPub)

	t.Run("BobReadsHisOwnRow", func(t *testing.T) {
		res, served := ask(strings.Repeat("a1", 32), bobPriv, attestation)
		require.True(t, served)
		require.Nil(t, res.Error, "a connection_key recipient with a valid attestation must be served: %v", res.Error)
		rows := rosterOf(res)
		require.Len(t, rows, 1, "scope=mine must give Bob exactly his own row")
		require.Equal(t, db.CashIdentityConnectionKey, rows[0].IdentityType)
		require.Equal(t, bobConnKey, rows[0].IdentityValue)
		require.EqualValues(t, 7000, rows[0].AmountMillis)
		t.Logf("Bob read his own row: %s=%s amount=%d", rows[0].IdentityType, rows[0].IdentityValue[:8], rows[0].AmountMillis)
	})

	t.Run("NoAttestationIsStillRefused", func(t *testing.T) {
		// The fix must not have been undone: without evidence, a token holder gets
		// nothing, which is what closed the roster disclosure.
		res, served := ask(strings.Repeat("a2", 32), bobPriv, "")
		require.True(t, served)
		require.NotNil(t, res.Error)
		require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code,
			"an attestation is the ONLY thing that resolves a connection_key caller; absent it, refuse")
	})

	t.Run("AnotherPersonsAttestationGrantsNothing", func(t *testing.T) {
		// A stranger holding the token, replaying an attestation that names Bob's
		// identity but was issued for a DIFFERENT keypair. The attestation binds an
		// identity to a KEY, and this item is signed by the wrong key.
		strangerPriv := nostr.GeneratePrivateKey()
		res, served := ask(strings.Repeat("a3", 32), strangerPriv, attestation)
		require.True(t, served)
		require.NotNil(t, res.Error,
			"an attestation issued for Bob's key must not authorize an item signed by someone else")
		require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)
	})

	t.Run("RevokedIAGrantsNothing", func(t *testing.T) {
		// Live trust, not trust-at-mint. An IA whose registration is withdrawn must
		// stop vouching for readers exactly as it stops vouching for redeemers —
		// otherwise revocation would close the spend path and leave the read open.
		require.NoError(t, iaMgr.Delete(iaPub))
		res, served := ask(strings.Repeat("a4", 32), bobPriv, attestation)
		require.True(t, served)
		require.NotNil(t, res.Error,
			"a revoked IA's attestation must not still authorize a read")
		require.Equal(t, constants.ERROR_NOT_FOUND, res.Error.Code)
	})
}

// buildIAAttestationEventFor mints a kind-35522 IA attestation binding a
// connection_key identity to a claimant's Nostr pubkey — the same shape
// cash_redeem already requires, carrying the platform/evidence tags
// VerifyClaimAttestationEvent checks.
func buildIAAttestationEventFor(t *testing.T, iaPrivkey, connectionKey, claimantPubkey string) string {
	t.Helper()
	ev := &nostr.Event{
		Kind:      35522,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", connectionKey},
			{"p", claimantPubkey},
			{"platform", "discord"},
			{"evidence", `{"version":1,"platform":"discord","auth_type":"public_post","user_id":"test-user"}`},
			// Required: VerifyClaimAttestationEvent refuses an attestation with no
			// expiration at all, so an IA cannot vouch for someone indefinitely.
			{"expiration", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)},
		},
	}
	require.NoError(t, ev.Sign(iaPrivkey))
	b, err := json.Marshal(ev)
	require.NoError(t, err)
	return string(b)
}
