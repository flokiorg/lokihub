package apps_test

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/tests"
)

func randomIAPubkey() string {
	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	return pk
}

func TestIdentityAuthorityManager_List_Empty(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	m := apps.NewIdentityAuthorityManager(svc.DB)
	result, err := m.List()
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestIdentityAuthorityManager_Add_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	m := apps.NewIdentityAuthorityManager(svc.DB)
	pubkey := randomIAPubkey()

	authority, err := m.Add(pubkey, "Test IA", []string{"wss://relay.one", "wss://relay.two"})
	require.NoError(t, err)
	assert.Equal(t, pubkey, authority.Pubkey)
	assert.Equal(t, "Test IA", authority.Name)
	assert.Equal(t, "wss://relay.one,wss://relay.two", authority.RelayURLs)

	result, err := m.List()
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, pubkey, result[0].Pubkey)
}

func TestIdentityAuthorityManager_Add_NormalizesCase(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	m := apps.NewIdentityAuthorityManager(svc.DB)
	pubkey := randomIAPubkey()

	authority, err := m.Add(strings.ToUpper(pubkey), "Test IA", nil)
	require.NoError(t, err)
	assert.Equal(t, pubkey, authority.Pubkey, "pubkey must be normalized to lowercase")
}

func TestIdentityAuthorityManager_Add_InvalidHex(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	m := apps.NewIdentityAuthorityManager(svc.DB)
	_, err = m.Add("not-valid-hex!", "Bad IA", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, apps.ErrInvalidIdentityAuthorityPubkey)
}

func TestIdentityAuthorityManager_Add_WrongLength(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	m := apps.NewIdentityAuthorityManager(svc.DB)
	_, err = m.Add("abcd", "Too Short", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, apps.ErrInvalidIdentityAuthorityPubkey)
}

func TestIdentityAuthorityManager_Add_Duplicate(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	m := apps.NewIdentityAuthorityManager(svc.DB)
	pubkey := randomIAPubkey()

	_, err = m.Add(pubkey, "First", nil)
	require.NoError(t, err)

	_, err = m.Add(pubkey, "Second", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, apps.ErrDuplicateIdentityAuthorityPubkey)
}

func TestIdentityAuthorityManager_Delete_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	m := apps.NewIdentityAuthorityManager(svc.DB)
	pubkey := randomIAPubkey()

	_, err = m.Add(pubkey, "Test IA", nil)
	require.NoError(t, err)

	require.NoError(t, m.Delete(pubkey))

	result, err := m.List()
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestIdentityAuthorityManager_Delete_NotFound(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	m := apps.NewIdentityAuthorityManager(svc.DB)
	err = m.Delete(randomIAPubkey())
	require.Error(t, err)
	assert.ErrorIs(t, err, apps.ErrIdentityAuthorityNotFound)
}

func TestIdentityAuthorityManager_IsTrusted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	m := apps.NewIdentityAuthorityManager(svc.DB)
	trustedPubkey := randomIAPubkey()
	untrustedPubkey := randomIAPubkey()

	_, err = m.Add(trustedPubkey, "Trusted", nil)
	require.NoError(t, err)

	trusted, err := m.IsTrusted(trustedPubkey)
	require.NoError(t, err)
	assert.True(t, trusted)

	trusted, err = m.IsTrusted(untrustedPubkey)
	require.NoError(t, err)
	assert.False(t, trusted)
}
