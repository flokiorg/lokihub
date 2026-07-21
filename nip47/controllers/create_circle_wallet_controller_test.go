package controllers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// buildCircleWalletIdentityEvent builds and signs a kind-35521 proof that the
// caller controls requesterPrivkey, bound to this specific hub via the d-tag
// — mirrors buildClaimProofEvent (claim_funds_controller_test.go) for the
// simpler circle case (always self-proof, no invoice/attestation binding).
func buildCircleWalletIdentityEvent(t *testing.T, requesterPrivkey, hubAppPubkey string) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{
		Kind:      nostrKindClaimProof,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"d", hubAppPubkey}},
	}
	require.NoError(t, ev.Sign(requesterPrivkey))
	return ev
}

// makeCircleWalletRequest builds a create_circle_wallet request JSON signed
// by requesterPrivkey and bound to hubAppPubkey. Marshals via
// createCircleWalletParams (rather than a hand-built string template) so the
// embedded identity_event JSON is escaped correctly — mirrors
// handleClaimFundsFor's content-map + json.Marshal pattern.
func makeCircleWalletRequest(t *testing.T, requesterPrivkey, hubAppPubkey string, maxAmountMloki uint64, expirationSecs int) string {
	t.Helper()
	requesterPubkey, _ := nostr.GetPublicKey(requesterPrivkey)
	params := createCircleWalletParams{
		RequesterPubkey: requesterPubkey,
		MaxAmount:       maxAmountMloki,
		Expiry:          expirationSecs,
		IdentityEvent:   mustMarshal(t, buildCircleWalletIdentityEvent(t, requesterPrivkey, hubAppPubkey)),
	}
	content := map[string]interface{}{"method": "create_circle_wallet", "params": params}
	b, err := json.Marshal(content)
	require.NoError(t, err)
	return string(b)
}

// createCircleHub creates a circle_hub app with the given config and pre-funds it.
// PerWalletMaxMloki is set high enough to never bind in these tests — the
// aggregate balance is the only ceiling they exercise. Tests that need to
// exercise the per-wallet cap or a renewal floor use createCircleHubWithCaps instead.
func createCircleHub(t *testing.T, svc *tests.TestService, maxExpSecs int, balanceMloki uint64) *db.App {
	t.Helper()
	return createCircleHubWithCaps(t, svc, maxExpSecs, balanceMloki, 1_000_000_000, "")
}

// createCircleHubWithCaps creates a circle_hub app with an explicit
// per-wallet amount ceiling and renewal floor, for tests exercising those caps
// specifically. An empty minBudgetRenewal defaults to "monthly" (CreateCircleHub's
// own default), matching the hub-config default a real admin would get.
func createCircleHubWithCaps(t *testing.T, svc *tests.TestService, maxExpSecs int, balanceMloki uint64,
	perWalletMaxMloki int, minBudgetRenewal string) *db.App {
	t.Helper()
	provider, _, err := svc.AppsService.CreateCircleHub(
		"Circle Hub",
		"",
		0,
		constants.BUDGET_RENEWAL_NEVER,
		nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "Circle Hub", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{
			MaxExpSecs:        maxExpSecs,
			FeesPpm:           0,
			PerWalletMaxMloki: perWalletMaxMloki,
			MinBudgetRenewal:  minBudgetRenewal,
		},
	)
	require.NoError(t, err)
	if balanceMloki > 0 {
		tests.FundApp(svc, provider.ID, balanceMloki, "fundtxhash")
	}
	return provider
}

// createCircleHubWithHubBudget creates a circle_hub app with a hub-level
// budget ceiling (its own AppPermission on the circle_wallet scope) —
// distinct from PerWalletMaxMloki, which caps each individual child. Balance
// is set high so the pre-existing commitment-vs-balance check never binds;
// these tests exercise the hub budget ceiling specifically. hubMaxAmountMloki
// is expressed in mloki like every other amount in this file, then converted
// to whole loki (CreateCircleHub's/AppPermission.MaxAmountLoki's native unit,
// same as a normal isolated wallet's pay_invoice budget) — pass a multiple of
// 1000 to avoid losing precision to that conversion.
func createCircleHubWithHubBudget(t *testing.T, svc *tests.TestService, balanceMloki uint64, hubMaxAmountMloki uint64) *db.App {
	t.Helper()
	provider, _, err := svc.AppsService.CreateCircleHub(
		"Circle Hub",
		"",
		hubMaxAmountMloki/1000,
		constants.BUDGET_RENEWAL_NEVER,
		nil,
		[]string{constants.CIRCLE_WALLET_SCOPE, constants.GET_BALANCE_SCOPE},
		nil,
		apps.CircleIdentityRef{Name: "Circle Hub", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{
			MaxExpSecs:        7200,
			FeesPpm:           0,
			PerWalletMaxMloki: 1_000_000_000,
			MinBudgetRenewal:  "",
		},
	)
	require.NoError(t, err)
	if balanceMloki > 0 {
		tests.FundApp(svc, provider.ID, balanceMloki, "fundtxhash")
	}
	return provider
}

func TestHandleCreateCircleWalletEvent_HubBudgetExceeded_Rejected(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Hub budget ceiling of 150_000 mloki, real balance far higher (10_000_000)
	// so the hub budget — not the balance — is the binding constraint.
	provider := createCircleHubWithHubBudget(t, svc, 10_000_000, 150_000)

	// Pre-commit 100_000 mloki via an existing active child.
	future := time.Now().Add(time.Hour)
	_, _, err = svc.AppsService.CreateApp("existing-child", "", 100, "never", &future,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &provider.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	requesterKey := nostr.GeneratePrivateKey()

	// Request 60_000 more mloki: 100_000 + 60_000 = 160_000 > 150_000 hub budget.
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 60_000, 3600)), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	require.NotNil(t, publishedResponse.Error, "commitment exceeding the hub's own budget ceiling must be rejected even though real balance is sufficient")
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, publishedResponse.Error.Code)
}

// Boundary case: committing exactly up to the hub budget must succeed; one
// mloki over must fail — mirrors TestHandleCreateCircleWalletEvent_CommitmentBoundary
// but for the hub-level ceiling instead of the real-balance ceiling.
func TestHandleCreateCircleWalletEvent_HubBudgetBoundary(t *testing.T) {
	ctx := context.TODO()

	callCreate := func(svc *tests.TestService, provider *db.App, amountMloki uint64) *models.Response {
		requesterKey := nostr.GeneratePrivateKey()
		nip47Request := &models.Request{}
		_ = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, amountMloki, 3600)), nip47Request)
		ev := &db.RequestEvent{}
		svc.DB.Create(&ev)
		var resp *models.Response
		NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
			HandleCreateCircleWalletEvent(ctx, nip47Request, ev.ID, provider, func(r *models.Response, _ nostr.Tags) { resp = r })
		return resp
	}

	t.Run("exactly_at_hub_budget_succeeds", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()
		provider := createCircleHubWithHubBudget(t, svc, 10_000_000, 100_000)
		resp := callCreate(svc, provider, 100_000)
		assert.Nil(t, resp.Error, "commitment == hub budget must succeed")
	})

	t.Run("one_over_hub_budget_fails", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()
		provider := createCircleHubWithHubBudget(t, svc, 10_000_000, 100_000)
		resp := callCreate(svc, provider, 100_001)
		require.NotNil(t, resp.Error, "commitment > hub budget must fail")
		assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, resp.Error.Code)
	})
}

// A zero hub MaxAmountLoki (the default — see createCircleHub) means "no
// hub-wide ceiling configured"; only the real-balance check should apply.
// This is exercised implicitly by every other test in this file via
// createCircleHub/createCircleHubWithCaps, both of which pass 0.
func TestHandleCreateCircleWalletEvent_HubBudgetUnset_OnlyBalanceApplies(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHubWithHubBudget(t, svc, 100_000, 0)

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 100_000, 3600)), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	assert.Nil(t, publishedResponse.Error, "an unset hub budget (0) must not block a request within the real balance")
}

func TestHandleCreateCircleWalletEvent_NotCircleHub(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	standardApp, _, err := svc.AppsService.CreateApp("std", "", 0, "never", nil, []string{constants.GET_INFO_SCOPE}, db.AppKindStandard, nil, "", nil)
	require.NoError(t, err)

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, standardApp.AppPubkey, 50_000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, standardApp, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_NOT_SUPPORTED, publishedResponse.Error.Code)
}

func TestHandleCreateCircleWalletEvent_Unauthorized(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 50_000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: false}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_RESTRICTED, publishedResponse.Error.Code)
}

func TestHandleCreateCircleWalletEvent_ExpiryExceedsMax(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 3600, 1_000_000) // max 3600 secs

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 50_000, 7200)), nip47Request) // 7200 > 3600
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
}

func TestHandleCreateCircleWalletEvent_CommitmentExceedsBalance(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 100_000) // 100_000 mloki total balance

	// Pre-commit 80_000 mloki via an existing active child.
	future := time.Now().Add(time.Hour)
	_, _, err = svc.AppsService.CreateApp("existing-child", "", 80, "never", &future,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &provider.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)

	requesterKey := nostr.GeneratePrivateKey()

	// Request 30_000 mloki but only 20_000 available (100k - 80k).
	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 30_000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, publishedResponse.Error.Code)
}

func TestHandleCreateCircleWalletEvent_RateLimited(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)

	requesterKey := nostr.GeneratePrivateKey()
	requesterPubkey, _ := nostr.GetPublicKey(requesterKey)

	controller := NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true})

	// Exhaust the rate limiter for this requester.
	for i := 0; i < circleRateLimitPerHour; i++ {
		controller.circleRateLimiter.Allow(requesterPubkey, circleRateLimitPerHour)
	}

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 10_000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	controller.HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
		publishedResponse = r
	})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, publishedResponse.Error.Code)
}

func TestHandleCreateCircleWalletEvent_HappyPath(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000) // 1_000_000 mloki balance

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 100_000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	assert.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(createCircleWalletResponse)
	assert.NotEmpty(t, result.EncryptedPairingURI)
	assert.NotEmpty(t, result.WalletPubkey)
	assert.Greater(t, result.ExpiresAt, time.Now().Unix())
	assert.Equal(t, 0, result.FeesPpm)

	// Verify the child was created with the correct kind/parent.
	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Find(&childApps)
	assert.Equal(t, 1, len(childApps))
	assert.Equal(t, db.ParentKindCircle, childApps[0].ParentKind)

	// Verify child has make_invoice scope.
	var makeInvoicePerm db.AppPermission
	result2 := svc.DB.Where("app_id = ? AND scope = ?", childApps[0].ID, constants.MAKE_INVOICE_SCOPE).First(&makeInvoicePerm)
	assert.NoError(t, result2.Error, "circle child must have make_invoice scope")

	// Verify circle child starts with zero balance.
	var incoming []db.Transaction
	svc.DB.Where("app_id = ? AND type = ? AND state = ?", childApps[0].ID, constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_SETTLED).Find(&incoming)
	assert.Empty(t, incoming, "circle child wallet must start with zero balance — no transfer happens at creation")
}

// E5: circle_wallet has make_invoice scope but must NOT have create_circle_wallet scope.
func TestHandleCreateCircleWalletEvent_ChildScopes(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 500_000)

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 100_000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(_ *models.Response, _ nostr.Tags) {})

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Find(&childApps)
	require.Len(t, childApps, 1)

	var perms []db.AppPermission
	svc.DB.Where("app_id = ?", childApps[0].ID).Find(&perms)

	hasPayInvoice := false
	for _, p := range perms {
		assert.NotEqual(t, constants.CIRCLE_WALLET_SCOPE, p.Scope, "circle_wallet child must not be able to issue sub-wallets")
		if p.Scope == constants.MAKE_INVOICE_SCOPE {
			hasPayInvoice = true
		}
	}
	assert.True(t, hasPayInvoice, "circle_wallet child must have make_invoice scope")
}

// E2: commitment boundary — exactly at balance must succeed; one over must fail.
func TestHandleCreateCircleWalletEvent_CommitmentBoundary(t *testing.T) {
	ctx := context.TODO()

	makeController := func(svc *tests.TestService) *nip47Controller {
		return NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true})
	}
	callCreate := func(svc *tests.TestService, provider *db.App, amountMloki uint64) *models.Response {
		requesterKey := nostr.GeneratePrivateKey()
		nip47Request := &models.Request{}
		_ = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, amountMloki, 3600)), nip47Request)
		ev := &db.RequestEvent{}
		svc.DB.Create(&ev)
		var resp *models.Response
		makeController(svc).HandleCreateCircleWalletEvent(ctx, nip47Request, ev.ID, provider, func(r *models.Response, _ nostr.Tags) { resp = r })
		return resp
	}

	t.Run("exactly_at_balance_succeeds", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()
		provider := createCircleHub(t, svc, 7200, 100_000)
		resp := callCreate(svc, provider, 100_000)
		assert.Nil(t, resp.Error, "commitment == balance must succeed")
	})

	t.Run("one_over_balance_fails", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()
		provider := createCircleHub(t, svc, 7200, 100_000)
		resp := callCreate(svc, provider, 100_001)
		assert.NotNil(t, resp.Error, "commitment > balance must fail")
		assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, resp.Error.Code)
	})
}

// E1: concurrent creation race — with balance for exactly one wallet, at most one must be committed.
func TestHandleCreateCircleWalletEvent_ConcurrentCreation_AtMostOneSucceeds(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Balance for exactly one wallet at 100_000 mloki.
	provider := createCircleHub(t, svc, 7200, 100_000)
	controller := NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true})

	newRequest := func() *models.Request {
		requesterKey := nostr.GeneratePrivateKey()
		r := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 100_000, 3600)), r))
		return r
	}
	newEvent := func() uint {
		ev := &db.RequestEvent{}
		svc.DB.Create(&ev)
		return ev.ID
	}

	const goroutines = 2
	type args struct {
		req     *models.Request
		eventID uint
	}
	prepared := make([]args, goroutines)
	for i := range prepared {
		prepared[i] = args{req: newRequest(), eventID: newEvent()}
	}

	responses := make(chan *models.Response, goroutines)
	ready := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		a := prepared[i]
		go func() {
			defer wg.Done()
			<-ready
			controller.HandleCreateCircleWalletEvent(ctx, a.req, a.eventID, provider,
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
	assert.LessOrEqual(t, successes, 1, "at most one concurrent create_circle_wallet should succeed")

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Find(&childApps)
	assert.LessOrEqual(t, len(childApps), 1, "at most one circle_wallet child should exist after concurrent creation")
}

// TestHandleCreateCircleWalletEvent_NonRoundMloki_CapRoundsDownSafely documents
// the effect of storing the cap as whole loki (sats): a max_amount that isn't a
// multiple of 1000 mloki has its enforced cap rounded DOWN when converted back
// to mloki in make_invoice_controller.go, never up. This means a recipient can
// receive up to 999 mloki (under 1 sat) less than the nominal max_amount, but
// the provider's actual exposure is never more than they configured — the
// truncation always favors the provider. This test locks in that direction.
func TestHandleCreateCircleWalletEvent_NonRoundMloki_CapRoundsDownSafely(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)

	requesterKey := nostr.GeneratePrivateKey()

	const requestedMaxAmount = uint64(1234567) // not a multiple of 1000
	const effectiveCapMloki = uint64(1234000)  // floor(1234567/1000) * 1000

	createReq := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, requestedMaxAmount, 3600)), createReq))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	controller := NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true})
	var createResp *models.Response
	controller.HandleCreateCircleWalletEvent(ctx, createReq, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
		createResp = r
	})
	require.Nil(t, createResp.Error)

	var child db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).First(&child).Error)

	// Receiving exactly the effective (rounded-down) cap must succeed...
	okReq := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeInvoiceRequestJSON(effectiveCapMloki)), okReq))
	okEvent := &db.RequestEvent{AppId: &child.ID, NostrId: nostr.GeneratePrivateKey()}
	svc.DB.Create(&okEvent)
	var okResp *models.Response
	controller.HandleMakeInvoiceEvent(ctx, okReq, okEvent.ID, child.ID, func(r *models.Response, _ nostr.Tags) {
		okResp = r
	})
	assert.Nil(t, okResp.Error, "receiving exactly the rounded-down cap must succeed")

	// ...but receiving even 1 mloki more must be rejected, proving the cap never
	// exceeds the provider's intended maximum despite the rounding.
	tooMuchReq := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeInvoiceRequestJSON(effectiveCapMloki+1)), tooMuchReq))
	tooMuchEvent := &db.RequestEvent{AppId: &child.ID, NostrId: nostr.GeneratePrivateKey()}
	svc.DB.Create(&tooMuchEvent)
	var tooMuchResp *models.Response
	controller.HandleMakeInvoiceEvent(ctx, tooMuchReq, tooMuchEvent.ID, child.ID, func(r *models.Response, _ nostr.Tags) {
		tooMuchResp = r
	})
	require.NotNil(t, tooMuchResp.Error)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, tooMuchResp.Error.Code)
}

// makeCircleWalletRequestWithRenewal is like makeCircleWalletRequest but also
// sets budget_renewal — omit it (pass "") to test the caller-omitted default.
func makeCircleWalletRequestWithRenewal(t *testing.T, requesterPrivkey, hubAppPubkey string, maxAmountMloki uint64, expirationSecs int, budgetRenewal string) string {
	t.Helper()
	requesterPubkey, _ := nostr.GetPublicKey(requesterPrivkey)
	params := createCircleWalletParams{
		RequesterPubkey: requesterPubkey,
		MaxAmount:       maxAmountMloki,
		Expiry:          expirationSecs,
		BudgetRenewal:   budgetRenewal,
		IdentityEvent:   mustMarshal(t, buildCircleWalletIdentityEvent(t, requesterPrivkey, hubAppPubkey)),
	}
	content := map[string]interface{}{"method": "create_circle_wallet", "params": params}
	b, err := json.Marshal(content)
	require.NoError(t, err)
	return string(b)
}

func TestHandleCreateCircleWalletEvent_ExpiryOmitted_DefaultsToMaxExpSecs(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000) // max_exp_secs = 7200

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 100_000, 0)), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	before := time.Now()
	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	require.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(createCircleWalletResponse)
	assert.InDelta(t, before.Add(7200*time.Second).Unix(), result.ExpiresAt, 5,
		"an omitted expiry must default to the hub's max_exp_secs, not produce an already-expired wallet")
}

func TestHandleCreateCircleWalletEvent_MaxAmountExceedsPerWalletMax_Rejected(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHubWithCaps(t, svc, 7200, 10_000_000, 100_000, "")

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 100_001, 3600)), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	require.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, publishedResponse.Error.Code)
}

func TestHandleCreateCircleWalletEvent_BudgetRenewal_AtOrLooserThanFloor_Accepted(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Hub floor is "monthly" — "monthly", "yearly", and "never" must all be accepted.
	provider := createCircleHubWithCaps(t, svc, 7200, 10_000_000, 1_000_000, constants.BUDGET_RENEWAL_MONTHLY)

	for _, renewal := range []string{constants.BUDGET_RENEWAL_MONTHLY, constants.BUDGET_RENEWAL_YEARLY, constants.BUDGET_RENEWAL_NEVER} {
		requesterKey := nostr.GeneratePrivateKey()

		nip47Request := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequestWithRenewal(t, requesterKey, provider.AppPubkey, 1000, 3600, renewal)), nip47Request))

		dbRequestEvent := &db.RequestEvent{}
		svc.DB.Create(&dbRequestEvent)

		var publishedResponse *models.Response
		NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
			HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
				publishedResponse = r
			})

		require.Nil(t, publishedResponse.Error, "renewal %q should be accepted under a monthly floor", renewal)
		result := publishedResponse.Result.(createCircleWalletResponse)
		assert.Equal(t, renewal, result.BudgetRenewal)

		var childApp db.App
		require.NoError(t, svc.DB.Where("wallet_pubkey = ?", result.WalletPubkey).First(&childApp).Error)
		var perm db.AppPermission
		require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", childApp.ID, constants.PAY_INVOICE_SCOPE).First(&perm).Error)
		assert.Equal(t, renewal, perm.BudgetRenewal)
	}
}

func TestHandleCreateCircleWalletEvent_BudgetRenewal_TighterThanFloor_Rejected(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Hub floor is "monthly" — "daily" and "weekly" are too frequent, must be rejected.
	provider := createCircleHubWithCaps(t, svc, 7200, 10_000_000, 1_000_000, constants.BUDGET_RENEWAL_MONTHLY)

	for _, renewal := range []string{constants.BUDGET_RENEWAL_DAILY, constants.BUDGET_RENEWAL_WEEKLY} {
		requesterKey := nostr.GeneratePrivateKey()

		nip47Request := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequestWithRenewal(t, requesterKey, provider.AppPubkey, 1000, 3600, renewal)), nip47Request))

		dbRequestEvent := &db.RequestEvent{}
		svc.DB.Create(&dbRequestEvent)

		var publishedResponse *models.Response
		NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
			HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
				publishedResponse = r
			})

		require.NotNil(t, publishedResponse.Error, "renewal %q should be rejected under a monthly floor", renewal)
		assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
	}
}

func TestHandleCreateCircleWalletEvent_BudgetRenewal_Omitted_DefaultsToNever(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Even with a tight "daily" floor, an omitted budget_renewal must default
	// to "never" — always compliant, since "never" outranks every floor.
	provider := createCircleHubWithCaps(t, svc, 7200, 10_000_000, 1_000_000, constants.BUDGET_RENEWAL_DAILY)

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, provider.AppPubkey, 1000, 3600)), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	require.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(createCircleWalletResponse)
	assert.Equal(t, constants.BUDGET_RENEWAL_NEVER, result.BudgetRenewal)
}

func TestHandleCreateCircleWalletEvent_BudgetRenewal_InvalidValue_Rejected(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequestWithRenewal(t, requesterKey, provider.AppPubkey, 1000, 3600, "fortnightly")), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	require.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
}

// MinBudgetRenewal is enforced only at the moment create_circle_wallet is
// called — tightening the hub's floor afterward must not retroactively
// affect a child minted under the previous, looser floor.
func TestHandleCreateCircleWalletEvent_BudgetRenewal_FloorChangeIsNotRetroactive(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Hub floor starts loose ("yearly"), so the child below can request "yearly".
	provider := createCircleHubWithCaps(t, svc, 7200, 10_000_000, 1_000_000, constants.BUDGET_RENEWAL_YEARLY)

	requesterKey := nostr.GeneratePrivateKey()
	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequestWithRenewal(t, requesterKey, provider.AppPubkey, 1000, 3600, constants.BUDGET_RENEWAL_YEARLY)), nip47Request))
	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		HandleCreateCircleWalletEvent(ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})
	require.Nil(t, publishedResponse.Error)

	var child db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).First(&child).Error)
	var perm db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", child.ID, constants.PAY_INVOICE_SCOPE).First(&perm).Error)
	require.Equal(t, constants.BUDGET_RENEWAL_YEARLY, perm.BudgetRenewal)

	// The hub owner now tightens the floor to "monthly" — stricter than the
	// existing child's own "yearly" renewal.
	monthly := constants.BUDGET_RENEWAL_MONTHLY
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, nil, nil, nil, &monthly))

	// The existing child's own renewal must be untouched: there is no
	// re-validation after creation, only at creation time.
	var permAfter db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", child.ID, constants.PAY_INVOICE_SCOPE).First(&permAfter).Error)
	assert.Equal(t, constants.BUDGET_RENEWAL_YEARLY, permAfter.BudgetRenewal,
		"raising the hub's min_budget_renewal floor must not retroactively change an existing child's own renewal")
}
