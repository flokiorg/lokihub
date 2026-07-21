package api

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
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
