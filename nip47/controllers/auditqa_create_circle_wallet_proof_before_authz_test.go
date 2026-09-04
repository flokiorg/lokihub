package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// spyingSocialCache wraps mockSocialCache and counts how many times
// IsAuthorized is actually invoked, so a test can assert not just the final
// response but whether the authorization check was ever reached at all.
type spyingSocialCache struct {
	mockSocialCache
	calls int
}

func (s *spyingSocialCache) IsAuthorized(ctx context.Context, requesterPubkey string, identity *db.CircleIdentity, gormDB *gorm.DB) (bool, error) {
	s.calls++
	return s.mockSocialCache.IsAuthorized(ctx, requesterPubkey, identity, gormDB)
}

// TestHandleCreateCircleWalletEvent_InvalidProof_NeverReachesAuthorizationCheck
// is a regression for the ordering invariant NIP-CW's §Identity Proof states
// explicitly: "Verification MUST run before the allowlist/following
// authorization check. This ordering is what closes an allowlist-membership
// oracle: an attacker who does not hold the target's private key MUST NOT be
// able to reach the authorization check at all, so the response cannot be
// used to probe list membership."
//
// HandleCreateCircleWalletEvent (create_circle_wallet_controller.go) does
// verify the kind-23199 proof (verifyCircleWalletIdentityEvent) strictly
// before calling controller.socialCache.IsAuthorized — confirmed by reading
// the handler — but no existing test asserts this ordering as an observable
// fact. Every existing "WrongSigner"/"WrongHub" test
// (create_circle_wallet_identity_test.go) only asserts the final error code,
// using the package's shared mockSocialCache{authorized: true}, which would
// silently mask a future regression that swapped the check order (e.g. a
// "fast fail on non-membership before doing expensive signature
// verification" optimization) — IsAuthorized would still return true either
// way in those tests, so the response looks identical regardless of which
// check actually ran first. This test uses a call-counting spy instead, so a
// regression that lets an invalid proof reach IsAuthorized fails loudly here
// even though the outer response (BAD_REQUEST) would look unchanged.
func TestHandleCreateCircleWalletEvent_InvalidProof_NeverReachesAuthorizationCheck(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)

	victimKey := nostr.GeneratePrivateKey()
	victimPubkey, _ := nostr.GetPublicKey(victimKey)
	attackerKey := nostr.GeneratePrivateKey()

	// Same forged-proof shape as TestHandleCreateCircleWalletEvent_IdentityEvent_WrongSigner_Rejected:
	// the attacker signs with their OWN key but claims the victim's pubkey.
	forgedEvent := buildCircleWalletIdentityEvent(t, attackerKey, provider.AppPubkey)
	requestJSON := rawCircleWalletRequest(t, victimPubkey, 100_000, 3600, mustMarshal(t, forgedEvent))

	// authorized: true is deliberate — if the ordering regressed and
	// IsAuthorized ran anyway, the request would otherwise still fail for an
	// unrelated reason (e.g. no wallet created) that could mask the real
	// finding. Setting it to true isolates exactly one thing: was
	// IsAuthorized ever invoked at all for a proof that never verified.
	spy := &spyingSocialCache{mockSocialCache: mockSocialCache{authorized: true}}

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(requestJSON), nip47Request))
	ev := &db.RequestEvent{}
	svc.DB.Create(&ev)

	var resp *models.Response
	NewTestNip47ControllerWithSocialCache(svc, spy).
		HandleCreateCircleWalletEvent(context.TODO(), nip47Request, ev.ID, provider, func(r *models.Response, _ nostr.Tags) {
			resp = r
		})

	require.NotNil(t, resp.Error, "an attacker signing as themselves must not be able to claim a different pubkey")
	assert.Equal(t, 0, spy.calls,
		"SECURITY: the authorization/allowlist check must never run for a proof that failed verification — "+
			"reaching it anyway reopens the allowlist-membership oracle NIP-CW's §Identity Proof ordering rule exists to close")
}
