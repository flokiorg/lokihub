package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// callCreateCircleWallet dispatches a raw request JSON (as built by
// makeCircleWalletRequest or hand-assembled for a malformed/adversarial case)
// against provider and returns the decoded response.
func callCreateCircleWallet(t *testing.T, svc *tests.TestService, provider *db.App, requestJSON string) *models.Response {
	t.Helper()
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(requestJSON), nip47Request))
	ev := &db.RequestEvent{}
	svc.DB.Create(&ev)
	var resp *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(context.TODO(), nip47Request, ev.ID, provider, func(r *models.Response, _ nostr.Tags) {
			resp = r
		})
	return resp
}

// rawCircleWalletRequest builds a create_circle_wallet request JSON with an
// arbitrary, pre-built identity_event string (rather than a freshly-signed,
// correctly-bound one) — used to exercise malformed/adversarial identity
// proofs that makeCircleWalletRequest can't express.
func rawCircleWalletRequest(t *testing.T, requesterPubkey string, maxAmountMloki uint64, expirationSecs int, identityEventJSON string) string {
	t.Helper()
	params := createCircleWalletParams{
		RequesterPubkey: requesterPubkey,
		MaxAmount:       maxAmountMloki,
		Expiry:          expirationSecs,
		IdentityEvent:   identityEventJSON,
	}
	content := map[string]interface{}{"method": "create_circle_wallet", "params": params}
	b, err := json.Marshal(content)
	require.NoError(t, err)
	return string(b)
}

func TestHandleCreateCircleWalletEvent_IdentityEvent_Missing_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)
	requesterKey := nostr.GeneratePrivateKey()
	requesterPubkey, _ := nostr.GetPublicKey(requesterKey)

	resp := callCreateCircleWallet(t, svc, provider, rawCircleWalletRequest(t, requesterPubkey, 100_000, 3600, ""))

	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
}

func TestHandleCreateCircleWalletEvent_IdentityEvent_NotJSON_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)
	requesterKey := nostr.GeneratePrivateKey()
	requesterPubkey, _ := nostr.GetPublicKey(requesterKey)

	resp := callCreateCircleWallet(t, svc, provider, rawCircleWalletRequest(t, requesterPubkey, 100_000, 3600, "not valid json"))

	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
}

// The direct regression test for the impersonation gap the audit found: an
// attacker who merely knows a victim's pubkey (but not their private key)
// cannot forge a proof on the victim's behalf.
func TestHandleCreateCircleWalletEvent_IdentityEvent_WrongSigner_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)

	victimKey := nostr.GeneratePrivateKey()
	victimPubkey, _ := nostr.GetPublicKey(victimKey)
	attackerKey := nostr.GeneratePrivateKey()

	// Attacker signs the proof with their OWN key, but claims to be the
	// victim's pubkey in the request params.
	forgedEvent := buildCircleWalletIdentityEvent(t, attackerKey, provider.AppPubkey)
	resp := callCreateCircleWallet(t, svc, provider,
		rawCircleWalletRequest(t, victimPubkey, 100_000, 3600, mustMarshal(t, forgedEvent)))

	require.NotNil(t, resp.Error, "an attacker signing as themselves must not be able to claim a different pubkey")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
}

// Cross-hub replay: a proof bound to a different hub's AppPubkey (e.g.
// captured on another circle_hub connection the requester also holds) must
// not be usable here.
func TestHandleCreateCircleWalletEvent_IdentityEvent_WrongHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)
	otherProvider := createCircleHub(t, svc, 7200, 1_000_000)
	require.NotEqual(t, provider.AppPubkey, otherProvider.AppPubkey)

	requesterKey := nostr.GeneratePrivateKey()
	requesterPubkey, _ := nostr.GetPublicKey(requesterKey)

	// Proof is validly signed by the requester, but bound to otherProvider's
	// AppPubkey, not provider's.
	crossHubEvent := buildCircleWalletIdentityEvent(t, requesterKey, otherProvider.AppPubkey)
	resp := callCreateCircleWallet(t, svc, provider,
		rawCircleWalletRequest(t, requesterPubkey, 100_000, 3600, mustMarshal(t, crossHubEvent)))

	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
}

func TestHandleCreateCircleWalletEvent_IdentityEvent_Stale_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)
	requesterKey := nostr.GeneratePrivateKey()
	requesterPubkey, _ := nostr.GetPublicKey(requesterKey)

	staleEvent := &nostr.Event{
		Kind:      nostrKindClaimProof,
		CreatedAt: nostr.Timestamp(time.Now().Add(-10 * time.Minute).Unix()),
		Tags:      nostr.Tags{{"d", provider.AppPubkey}},
	}
	require.NoError(t, staleEvent.Sign(requesterKey))

	resp := callCreateCircleWallet(t, svc, provider,
		rawCircleWalletRequest(t, requesterPubkey, 100_000, 3600, mustMarshal(t, staleEvent)))

	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
}

func TestHandleCreateCircleWalletEvent_IdentityEvent_FutureTimestamp_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)
	requesterKey := nostr.GeneratePrivateKey()
	requesterPubkey, _ := nostr.GetPublicKey(requesterKey)

	futureEvent := &nostr.Event{
		Kind:      nostrKindClaimProof,
		CreatedAt: nostr.Timestamp(time.Now().Add(10 * time.Minute).Unix()),
		Tags:      nostr.Tags{{"d", provider.AppPubkey}},
	}
	require.NoError(t, futureEvent.Sign(requesterKey))

	resp := callCreateCircleWallet(t, svc, provider,
		rawCircleWalletRequest(t, requesterPubkey, 100_000, 3600, mustMarshal(t, futureEvent)))

	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
}

// A captured proof (the circle_hub connection is shared/public, so anyone
// holding it can decrypt every request sent over it, including this one)
// must not be resubmittable — the second attempt with the identical event
// must fail even though it would otherwise still be within its freshness
// window.
func TestHandleCreateCircleWalletEvent_IdentityEvent_Replayed_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)
	requesterKey := nostr.GeneratePrivateKey()
	requesterPubkey, _ := nostr.GetPublicKey(requesterKey)

	identityEvent := buildCircleWalletIdentityEvent(t, requesterKey, provider.AppPubkey)
	identityEventJSON := mustMarshal(t, identityEvent)

	first := callCreateCircleWallet(t, svc, provider, rawCircleWalletRequest(t, requesterPubkey, 100_000, 3600, identityEventJSON))
	require.Nil(t, first.Error, "first use of a fresh proof must succeed")

	// Second request reuses the identical identity_event (same signature,
	// same event ID) — a different, second identity (so it doesn't also
	// collide with the one-active-wallet-per-identity cap) reusing the FIRST
	// identity's proof would be a forgery; here we use the same identity to
	// isolate the replay-guard behavior specifically, so the expected
	// rejection reason is unambiguous.
	second := callCreateCircleWallet(t, svc, provider, rawCircleWalletRequest(t, requesterPubkey, 100_000, 3600, identityEventJSON))
	require.NotNil(t, second.Error, "a replayed identity_event must be rejected")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, second.Error.Code)
}

// CRITICAL regression test: go-nostr's CheckSignature() verifies the
// signature against a hash it recomputes from the event's own fields — it
// never checks that the client-supplied `id` field actually equals that
// hash (only CheckID() does). Since the replay guard trusts
// identityEvent.ID as its unique key, an attacker holding one captured,
// validly-signed proof could otherwise resubmit it indefinitely by mutating
// only the `id` field: the signature stays valid (it doesn't cover `id`),
// so the "already used" guard would never fire. This proves the fix
// (verifyCircleWalletIdentityEvent calling ev.CheckID()) actually closes it.
func TestHandleCreateCircleWalletEvent_IdentityEvent_TamperedID_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)
	requesterKey := nostr.GeneratePrivateKey()
	requesterPubkey, _ := nostr.GetPublicKey(requesterKey)

	genuine := buildCircleWalletIdentityEvent(t, requesterKey, provider.AppPubkey)

	// Simulate a captured proof resubmitted with the `id` field mutated to a
	// fresh, arbitrary value — everything else (including the real
	// signature) is untouched.
	tampered := *genuine
	tampered.ID = "1111111111111111111111111111111111111111111111111111111111111111"[:64]
	require.NotEqual(t, genuine.ID, tampered.ID)

	// The signature itself is still cryptographically valid for the
	// (unchanged) signed content — CheckSignature alone would accept this.
	valid, sigErr := tampered.CheckSignature()
	require.NoError(t, sigErr)
	require.True(t, valid, "sanity check: signature validity is independent of the id field")

	resp := callCreateCircleWallet(t, svc, provider,
		rawCircleWalletRequest(t, requesterPubkey, 100_000, 3600, mustMarshal(t, &tampered)))

	require.NotNil(t, resp.Error, "an event whose id doesn't match its own content must be rejected regardless of a valid signature")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
}

// Two different requesters signing independently, each with their own fresh
// proof bound to the same hub, must not collide with each other's replay
// guard entries (event IDs are content-addressed and differ by signer).
func TestHandleCreateCircleWalletEvent_IdentityEvent_DifferentRequesters_BothSucceed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)

	key1 := nostr.GeneratePrivateKey()
	pub1, _ := nostr.GetPublicKey(key1)
	key2 := nostr.GeneratePrivateKey()
	pub2, _ := nostr.GetPublicKey(key2)

	resp1 := callCreateCircleWallet(t, svc, provider, makeCircleWalletRequest(t, key1, provider.AppPubkey, 100_000, 3600))
	require.Nil(t, resp1.Error)
	resp2 := callCreateCircleWallet(t, svc, provider, makeCircleWalletRequest(t, key2, provider.AppPubkey, 100_000, 3600))
	require.Nil(t, resp2.Error)

	assert.NotEqual(t, pub1, pub2)
}

// Identity verification (step 1b/1c) runs before rate limiting (step 5), so
// a rejected impersonation attempt for a victim's pubkey never reaches
// controller.circleRateLimiter.Allow(victimPubkey, ...) at all — the victim's
// own rate-limit bucket must be completely untouched by any number of failed
// impersonation attempts against them. This is the practical payoff of the
// identity-binding fix: it isn't just that impersonation itself fails, but
// that it can no longer be used to grief a real member's rate limit.
func TestHandleCreateCircleWalletEvent_RejectedImpersonation_DoesNotConsumeVictimRateLimit(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)

	victimKey := nostr.GeneratePrivateKey()
	victimPubkey, _ := nostr.GetPublicKey(victimKey)

	// Attacker hammers create_circle_wallet claiming to be the victim, well
	// past the real 3/hour limit, using a fresh attacker key each time (so
	// none of these collide with the identity-proof replay guard either).
	for i := 0; i < circleRateLimitPerHour+5; i++ {
		attackerKey := nostr.GeneratePrivateKey()
		forgedEvent := buildCircleWalletIdentityEvent(t, attackerKey, provider.AppPubkey)
		resp := callCreateCircleWallet(t, svc, provider,
			rawCircleWalletRequest(t, victimPubkey, 50_000, 3600, mustMarshal(t, forgedEvent)))
		require.NotNil(t, resp.Error, "forged attempt %d must be rejected", i)
		assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
	}

	// The victim, using their own real key, must still be able to mint —
	// none of the above attempts should have consumed their rate-limit quota.
	victimResp := callCreateCircleWallet(t, svc, provider, makeCircleWalletRequest(t, victimKey, provider.AppPubkey, 50_000, 3600))
	assert.Nil(t, victimResp.Error, "the real victim's own rate limit must be untouched by rejected impersonation attempts")
}
