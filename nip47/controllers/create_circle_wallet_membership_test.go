package controllers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// A second create_circle_wallet call for the same identity, while their
// first wallet is still active, must be rejected — the ergonomic bug the
// user named directly, and also a real quota-bypass: without this, an
// identity could mint N wallets at PerWalletMaxMloki each to obtain N times
// the hub's intended per-member ceiling.
func TestHandleCreateCircleWalletEvent_Membership_SecondAttemptWhileActive_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)
	requesterKey := nostr.GeneratePrivateKey()

	// Each call needs its own distinct identity_event: nostr.Now() only has
	// second precision, so two plain makeCircleWalletRequest calls for the
	// same identity built within the same wall-clock second would be
	// byte-identical and collide on the single-use replay guard instead of
	// exercising the membership cap this test actually means to isolate.
	first := callCreateCircleWallet(t, svc, provider, distinctCircleWalletRequest(t, requesterKey, provider.AppPubkey, "first", 100_000, 3600))
	require.Nil(t, first.Error, "first mint for this identity must succeed")

	second := callCreateCircleWallet(t, svc, provider, distinctCircleWalletRequest(t, requesterKey, provider.AppPubkey, "second", 100_000, 3600))
	require.NotNil(t, second.Error, "a second mint for the same identity while the first wallet is active must be rejected")
	assert.Equal(t, constants.ERROR_RESTRICTED, second.Error.Code)

	var childCount int64
	svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Count(&childCount)
	assert.EqualValues(t, 1, childCount, "only the first wallet should exist")
}

// Direct regression test for the quota-bypass framing named above: N attempts
// at exactly PerWalletMaxMloki each must not accumulate past one wallet's
// worth of commitment.
func TestHandleCreateCircleWalletEvent_Membership_RepeatedAttempts_NeverExceedsPerWalletCap(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const perWalletMaxMloki = 100_000
	provider := createCircleHubWithCaps(t, svc, 7200, 10_000_000, perWalletMaxMloki, "")
	requesterKey := nostr.GeneratePrivateKey()

	var successes int
	for i := 0; i < 5; i++ {
		req := distinctCircleWalletRequest(t, requesterKey, provider.AppPubkey, string(rune('a'+i)), perWalletMaxMloki, 3600)
		resp := callCreateCircleWallet(t, svc, provider, req)
		if resp.Error == nil {
			successes++
		} else {
			assert.Equal(t, constants.ERROR_RESTRICTED, resp.Error.Code)
		}
	}
	assert.Equal(t, 1, successes, "only the first of N repeated attempts for the same identity should succeed")

	var totalCommitment int64
	var children []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Find(&children)
	for _, child := range children {
		var perm db.AppPermission
		require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", child.ID, constants.PAY_INVOICE_SCOPE).First(&perm).Error)
		totalCommitment += int64(perm.MaxAmountLoki) * 1000
	}
	assert.LessOrEqual(t, totalCommitment, int64(perWalletMaxMloki),
		"aggregate committed amount across every wallet this identity obtained must never exceed one wallet's cap")
}

// Once the identity's wallet is gone (expired-and-cleaned-up or manually
// deleted — both delete the child App row the same way), the cap is freed:
// this proves the cap is a concurrency limit, not a lifetime one.
func TestHandleCreateCircleWalletEvent_Membership_FreedAfterWalletDeleted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)
	requesterKey := nostr.GeneratePrivateKey()

	// Distinct identity_events per call — see the same note in
	// TestHandleCreateCircleWalletEvent_Membership_SecondAttemptWhileActive_Rejected.
	first := callCreateCircleWallet(t, svc, provider, distinctCircleWalletRequest(t, requesterKey, provider.AppPubkey, "first", 100_000, 3600))
	require.Nil(t, first.Error)

	var child db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).First(&child).Error)

	// Simulate the child having been deleted (expiry sweep or manual delete
	// both do a plain `DELETE FROM apps`, which cascades to the membership
	// row via its WalletAppID FK — see db.CircleWalletMembership).
	require.NoError(t, svc.DB.Delete(&db.App{}, child.ID).Error)

	var membershipCount int64
	svc.DB.Model(&db.CircleWalletMembership{}).
		Where("circle_hub_app_id = ? AND requester_pubkey = ?", provider.ID, mustPubkeyForTest(t, requesterKey)).
		Count(&membershipCount)
	require.EqualValues(t, 0, membershipCount, "deleting the wallet must cascade-delete its membership row")

	second := callCreateCircleWallet(t, svc, provider, distinctCircleWalletRequest(t, requesterKey, provider.AppPubkey, "second", 100_000, 3600))
	assert.Nil(t, second.Error, "the identity must be able to mint again once their previous wallet is gone")
}

func mustPubkeyForTest(t *testing.T, privkey string) string {
	t.Helper()
	pub, err := nostr.GetPublicKey(privkey)
	require.NoError(t, err)
	return pub
}

// distinctCircleWalletRequest is like makeCircleWalletRequest but the
// identity_event carries an extra disambiguating tag, guaranteeing a unique
// nostr event ID even when built repeatedly within the same wall-clock
// second (nostr.Now() has only second precision) — needed whenever a test
// calls create_circle_wallet more than once for the same identity in a tight
// loop, so the single-use replay guard never fires and only the behavior
// actually under test (e.g. the one-active-wallet-per-identity cap) governs
// the outcome.
func distinctCircleWalletRequest(t *testing.T, requesterPrivkey, hubAppPubkey, disambiguator string, maxAmountMloki uint64, expirationSecs int) string {
	t.Helper()
	requesterPubkey, _ := nostr.GetPublicKey(requesterPrivkey)
	identityEvent := &nostr.Event{
		Kind:      nostrKindClaimProof,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"d", hubAppPubkey}, {"disambiguator", disambiguator}},
	}
	require.NoError(t, identityEvent.Sign(requesterPrivkey))

	params := createCircleWalletParams{
		RequesterPubkey: requesterPubkey,
		MaxAmount:       maxAmountMloki,
		Expiry:          expirationSecs,
		IdentityEvent:   mustMarshal(t, identityEvent),
	}
	content := map[string]interface{}{"method": "create_circle_wallet", "params": params}
	b, err := json.Marshal(content)
	require.NoError(t, err)
	return string(b)
}

// Concurrent double-submit race for the same identity: with generous balance
// (so the balance/commitment check never binds), the membership guard must
// still allow exactly one success.
func TestHandleCreateCircleWalletEvent_Membership_ConcurrentRace_ExactlyOneWinner(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)
	requesterKey := nostr.GeneratePrivateKey()
	controller := NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true})

	const goroutines = 5
	requests := make([]*models.Request, goroutines)
	eventIDs := make([]uint, goroutines)
	for i := 0; i < goroutines; i++ {
		// Each goroutine's identity_event needs a distinct nostr event ID
		// (distinctCircleWalletRequest's disambiguator tag forces that even
		// if two are signed within the same wall-clock second) so this test
		// isolates the membership guard specifically, rather than
		// incidentally racing the identity-proof replay guard instead.
		reqJSON := distinctCircleWalletRequest(t, requesterKey, provider.AppPubkey, string(rune('a'+i)), 50_000, 3600)
		req := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(reqJSON), req))
		requests[i] = req
		ev := &db.RequestEvent{}
		svc.DB.Create(&ev)
		eventIDs[i] = ev.ID
	}

	responses := make(chan *models.Response, goroutines)
	ready := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			<-ready
			controller.HandleCreateCircleWalletEvent(context.TODO(), requests[i], eventIDs[i], provider,
				func(r *models.Response, _ nostr.Tags) { responses <- r })
		}()
	}
	close(ready)
	wg.Wait()
	close(responses)

	var successes int
	for r := range responses {
		if r.Error == nil {
			successes++
		}
	}
	assert.Equal(t, 1, successes, "exactly one concurrent create_circle_wallet for the same identity should succeed")

	var childCount int64
	svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Count(&childCount)
	assert.EqualValues(t, 1, childCount)

	var membershipCount int64
	svc.DB.Model(&db.CircleWalletMembership{}).
		Where("circle_hub_app_id = ? AND requester_pubkey = ?", provider.ID, mustPubkeyForTest(t, requesterKey)).
		Count(&membershipCount)
	assert.EqualValues(t, 1, membershipCount)
}
