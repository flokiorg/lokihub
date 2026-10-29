package nip47

import (
	"context"
	"encoding/json"
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

// NIP-CASH §Scoping the Roster: "mine" comes from the item proof's own signer,
// never from anything the item asserts (no attestation_event), and a caller whose
// proof verifies but who holds no slice receives no rows: NOT_FOUND, never a wider
// roster. A connection_key slice's identity_value is a hash, never a signer, so its
// holder gets NOT_FOUND for every scope (a spec gap logged for the owner); a pubkey
// holder's "mine" is its own row only.
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
