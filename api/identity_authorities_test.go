package api

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func newTestAPIWithIAManager(svc *tests.TestService) *api {
	return &api{db: svc.DB, appsSvc: svc.AppsService, keys: svc.Keys, cfg: svc.Cfg,
		iaManager: apps.NewIdentityAuthorityManager(svc.DB)}
}

func TestListIdentityAuthorities_Empty(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	theAPI := newTestAPIWithIAManager(svc)
	result, err := theAPI.ListIdentityAuthorities()
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestAddIdentityAuthority_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	theAPI := newTestAPIWithIAManager(svc)

	added, err := theAPI.AddIdentityAuthority(&AddIdentityAuthorityRequest{
		Pubkey:    pubkey,
		Name:      "Trusted IA",
		RelayURLs: []string{"wss://relay.one", "wss://relay.two"},
	})
	require.NoError(t, err)
	assert.Equal(t, pubkey, added.Pubkey)
	assert.Equal(t, "Trusted IA", added.Name)
	assert.Equal(t, []string{"wss://relay.one", "wss://relay.two"}, added.RelayURLs)
	assert.Greater(t, added.CreatedAt, int64(0))

	result, err := theAPI.ListIdentityAuthorities()
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, pubkey, result[0].Pubkey)
}

func TestAddIdentityAuthority_InvalidHex(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	theAPI := newTestAPIWithIAManager(svc)
	_, err = theAPI.AddIdentityAuthority(&AddIdentityAuthorityRequest{
		Pubkey: "not-valid-hex!",
		Name:   "Bad IA",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, apps.ErrInvalidIdentityAuthorityPubkey)
}

func TestAddIdentityAuthority_Duplicate(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	theAPI := newTestAPIWithIAManager(svc)

	_, err = theAPI.AddIdentityAuthority(&AddIdentityAuthorityRequest{Pubkey: pubkey, Name: "IA"})
	require.NoError(t, err)

	_, err = theAPI.AddIdentityAuthority(&AddIdentityAuthorityRequest{Pubkey: pubkey, Name: "IA again"})
	require.Error(t, err)
	assert.ErrorIs(t, err, apps.ErrDuplicateIdentityAuthorityPubkey)
}

func TestDeleteIdentityAuthority_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	theAPI := newTestAPIWithIAManager(svc)

	_, err = theAPI.AddIdentityAuthority(&AddIdentityAuthorityRequest{Pubkey: pubkey, Name: "IA"})
	require.NoError(t, err)

	require.NoError(t, theAPI.DeleteIdentityAuthority(pubkey))

	result, err := theAPI.ListIdentityAuthorities()
	require.NoError(t, err)
	assert.Empty(t, result)
}

// TestListIdentityAuthorities_UnredeemedSliceCount verifies the settings
// screen's IA-revocation blast-radius count: it must count only currently-
// unredeemed connection_key slices attesting to THAT specific IA — not
// already-redeemed ones, not pubkey/bearer slices (which carry no IAPubkey
// at all), and not another IA's own slices.
func TestListIdentityAuthorities_UnredeemedSliceCount(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	iaA, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	iaB, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	theAPI := newTestAPIWithIAManager(svc)

	_, err = theAPI.AddIdentityAuthority(&AddIdentityAuthorityRequest{Pubkey: iaA, Name: "IA with no claims yet"})
	require.NoError(t, err)
	_, err = theAPI.AddIdentityAuthority(&AddIdentityAuthorityRequest{Pubkey: iaB, Name: "IA with one claim"})
	require.NoError(t, err)

	// A freshly-registered IA with nothing attesting for it yet must report 0,
	// not error or omit the field.
	result, err := theAPI.ListIdentityAuthorities()
	require.NoError(t, err)
	require.Len(t, result, 2)
	for _, ia := range result {
		assert.Zero(t, ia.UnredeemedSliceCount, "pubkey=%s", ia.Pubkey)
	}

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 100_000)
	claimedAt := time.Now()

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		// Two unredeemed connection_key slices attesting to iaA.
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: tests.RandomHex32(), IAPubkey: iaA, AmountMloki: 1000},
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: tests.RandomHex32(), IAPubkey: iaA, AmountMloki: 1000},
		// A THIRD connection_key slice attesting to iaA, but already redeemed —
		// must NOT count toward the live blast radius.
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: tests.RandomHex32(), IAPubkey: iaA, AmountMloki: 1000, ClaimedAt: &claimedAt},
		// One unredeemed connection_key slice attesting to iaB — a wholly
		// separate IA's count must not bleed into iaA's, or vice versa.
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: tests.RandomHex32(), IAPubkey: iaB, AmountMloki: 1000},
		// A pubkey and a bearer slice on the SAME wallet, both with no
		// IAPubkey at all — must never contribute to any IA's count.
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 1000},
		{IdentityType: db.CashIdentityBearer, IdentityValue: tests.RandomHex32(), AmountMloki: 1000},
	}))

	result, err = theAPI.ListIdentityAuthorities()
	require.NoError(t, err)
	require.Len(t, result, 2)

	byPubkey := make(map[string]int)
	for _, ia := range result {
		byPubkey[ia.Pubkey] = ia.UnredeemedSliceCount
	}
	assert.Equal(t, 2, byPubkey[iaA], "iaA must count its 2 unredeemed slices, excluding the redeemed one")
	assert.Equal(t, 1, byPubkey[iaB], "iaB's count must be independent of iaA's")
}

func TestDeleteIdentityAuthority_NotFound(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	pubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	theAPI := newTestAPIWithIAManager(svc)

	err = theAPI.DeleteIdentityAuthority(pubkey)
	require.Error(t, err)
	assert.ErrorIs(t, err, apps.ErrIdentityAuthorityNotFound)
}
