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
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// decryptCircleWalletDetails opens a join response's encrypted_details as the
// joining member would: NIP-44, keyed to the member's own identity privkey and
// the circle hub wallet's pubkey.
func decryptCircleWalletDetails(t *testing.T, recipientPrivKey, hubWalletPubkey, encrypted string) circleWalletDetails {
	t.Helper()
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, hubWalletPubkey, recipientPrivKey)
	require.NoError(t, err)
	plaintext, err := c.Decrypt(encrypted)
	require.NoError(t, err)
	var details circleWalletDetails
	require.NoError(t, json.Unmarshal([]byte(plaintext), &details))
	return details
}

// joinCircle drives one create_circle_wallet call and returns the raw response.
func joinCircle(t *testing.T, ctx context.Context, svc *tests.TestService, provider *db.App, requesterKey string) *models.Response {
	t.Helper()
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal(
		[]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, 100_000, 3600)), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

	var published *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			published = r
		})
	require.NotNil(t, published)
	return published
}

// TestCreateCircleWallet_DetailsUnreadableByCoMember is the security property.
//
// create_circle_wallet is called over the *shared* circlehub connection, so the
// NIP-47 response envelope is encrypted to a key every circle member holds.
// Anything left outside the nested encryption is therefore readable by every
// other member — and WalletPubkey is the worst thing to leak, because it
// appears in the clear `p` tag of every subsequent call that member's wallet
// makes, so a co-member who learns it can follow that member for the wallet's
// whole life.
//
// Asserts both directions: the joiner can open its own details, and a second
// member holding the same shared hub connection cannot.
func TestCreateCircleWallet_DetailsUnreadableByCoMember(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)

	aliceKey := nostr.GeneratePrivateKey()
	bobKey := nostr.GeneratePrivateKey()

	published := joinCircle(t, ctx, svc, provider, aliceKey)
	require.Nil(t, published.Error)
	result, ok := published.Result.(createCircleWalletResponse)
	require.True(t, ok)
	require.NotEmpty(t, result.EncryptedDetails)

	// Alice — the joiner — can read her own details.
	details := decryptCircleWalletDetails(t, aliceKey, *provider.WalletPubkey, result.EncryptedDetails)
	assert.NotEmpty(t, details.WalletPubkey, "the joiner must be able to recover her wallet pubkey")
	assert.NotEmpty(t, details.PairingURI)

	// Bob holds the same shared circle hub connection, so he sees this exact
	// response on the wire. He must not be able to open it.
	bobCipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, *provider.WalletPubkey, bobKey)
	require.NoError(t, err)
	_, err = bobCipher.Decrypt(result.EncryptedDetails)
	assert.Error(t, err, "a co-member of the circle must not be able to decrypt another member's join details")
}

// TestCreateCircleWallet_ResponseCarriesNothingButCiphertext pins the shape that
// makes the property above hold. Any plaintext field added to the response is
// public to the whole circle, so the response must serialize to exactly one
// key. This fails loudly if someone reintroduces wallet_pubkey or the terms
// alongside the ciphertext.
func TestCreateCircleWallet_ResponseCarriesNothingButCiphertext(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)

	requesterKey := nostr.GeneratePrivateKey()
	published := joinCircle(t, ctx, svc, provider, requesterKey)
	require.Nil(t, published.Error)

	raw, err := json.Marshal(published.Result)
	require.NoError(t, err)

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))

	require.Len(t, fields, 1, "join response must carry only encrypted_details, got %v", fields)
	_, hasDetails := fields["encrypted_details"]
	assert.True(t, hasDetails, "expected encrypted_details, got %v", fields)

	// Belt-and-braces against a rename that keeps the leak: the member's wallet
	// pubkey must not appear anywhere in the serialized response.
	result, ok := published.Result.(createCircleWalletResponse)
	require.True(t, ok)
	details := decryptCircleWalletDetails(t, requesterKey, *provider.WalletPubkey, result.EncryptedDetails)
	require.NotEmpty(t, details.WalletPubkey)
	assert.NotContains(t, string(raw), details.WalletPubkey,
		"the member's wallet pubkey must not be readable in the response envelope")
}
