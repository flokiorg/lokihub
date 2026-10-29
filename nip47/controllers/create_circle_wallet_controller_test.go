package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// buildCircleWalletIdentityEvent builds and signs a kind-23199 proof that the
// caller controls requesterPrivkey, bound to this specific hub via the d-tag
// — mirrors buildClaimProofEvent (cash_redeem_controller_test.go) for the
// simpler circle case (always self-proof, no invoice/attestation binding).
func buildCircleWalletIdentityEvent(t *testing.T, requesterPrivkey, hubWalletPubkey string) *nostr.Event {
	t.Helper()
	requesterPubkey, err := nostr.GetPublicKey(requesterPrivkey)
	require.NoError(t, err)
	ev := &nostr.Event{
		Kind:      nostrKindCircleIdentityProof,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"d", hubWalletPubkey}, {"connection", testConnectionPubkey(t, requesterPubkey)}},
	}
	require.NoError(t, ev.Sign(requesterPrivkey))
	joinProofSigners.Store(ev.ID, requesterPrivkey)
	return ev
}

// joinProofSigners remembers who signed each join proof a test built, so testJoin
// can bind it to the request's terms (NIP-CW §Identity Proof, check 7).
var joinProofSigners sync.Map

// testJoin serves a join the way the tests build it: a proof built by
// buildCircleWalletIdentityEvent gets the request's terms tag and is re-signed
// by its signer, and a request without max_receive asks for as much as
// max_amount. A proof the tests did not sign is sent as it is.
func (controller *nip47Controller) testJoin(t testing.TB, ctx context.Context, req *models.Request, requestEventId uint, app *db.App, publish publishFunc) {
	t.Helper()
	controller.HandleCreateCircleWalletEvent(ctx, bindJoinTerms(t, req), requestEventId, app, publish)
}

func bindJoinTerms(t testing.TB, req *models.Request) *models.Request {
	t.Helper()
	var params map[string]any
	if json.Unmarshal(req.Params, &params) != nil || params == nil {
		return req
	}
	if _, ok := params["max_receive"]; !ok {
		params["max_receive"] = params["max_amount"]
	}
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	parsed, err := nipcw.ParseCreateCircleWalletRequest(raw)
	if err == nil {
		var ev nostr.Event
		if json.Unmarshal([]byte(parsed.IdentityEvent), &ev) == nil {
			if priv, ok := joinProofSigners.Load(ev.ID); ok {
				if terms, err := nipcw.TermsTag(parsed.Terms()); err == nil {
					kept := nostr.Tags{}
					for _, tag := range ev.Tags {
						if len(tag) > 0 && tag[0] != "terms" {
							kept = append(kept, tag)
						}
					}
					ev.Tags = append(kept, nostr.Tag{"terms", terms})
					require.NoError(t, ev.Sign(priv.(string)))
					joinProofSigners.Store(ev.ID, priv)
					evJSON, err := json.Marshal(ev)
					require.NoError(t, err)
					params["identity_event"] = string(evJSON)
				}
			}
		}
	}
	raw, err = json.Marshal(params)
	require.NoError(t, err)
	return &models.Request{Method: req.Method, Params: raw}
}

// joinRequestJSON is a create_circle_wallet request carrying params, with an
// empty budget_renewal left out: absent asks for the Hub default, while an
// explicit "" is refused (NIP-CW §Request).
func joinRequestJSON(t testing.TB, params nipcw.CreateCircleWalletRequest) string {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	if params.BudgetRenewal == "" {
		delete(fields, "budget_renewal")
	}
	b, err := json.Marshal(map[string]any{"method": constants.NIP47MethodCreateCircleWallet, "params": fields})
	require.NoError(t, err)
	return string(b)
}

// testConnectionSecret and testConnectionPubkey are the member-generated
// connection key (NIP-CW §Request) the test helpers use for a requester. It is
// derived from the requester's pubkey, so every helper and every hand-built
// proof agrees on it without passing it around. Real clients generate a fresh
// one per request; the nmilat-built tests do exactly that.
func testConnectionSecret(requesterPubkey string) string {
	sum := sha256.Sum256([]byte("connection:" + requesterPubkey))
	return hex.EncodeToString(sum[:])
}

func testConnectionPubkey(t testing.TB, requesterPubkey string) string {
	t.Helper()
	pub, err := nostr.GetPublicKey(testConnectionSecret(requesterPubkey))
	require.NoError(t, err)
	return pub
}

// makeCircleWalletRequest builds a create_circle_wallet request JSON signed
// by requesterPrivkey and bound to hubWalletPubkey. Marshals via
// createCircleWalletParams (rather than a hand-built string template) so the
// embedded identity_event JSON is escaped correctly — mirrors
// handleClaimFundsFor's content-map + json.Marshal pattern.
func makeCircleWalletRequest(t *testing.T, requesterPrivkey, hubWalletPubkey string, maxAmountMloki uint64, expirationSecs uint64) string {
	t.Helper()
	requesterPubkey, _ := nostr.GetPublicKey(requesterPrivkey)
	params := nipcw.CreateCircleWalletRequest{
		Pubkey:           requesterPubkey,
		ConnectionPubkey: testConnectionPubkey(t, requesterPubkey),
		MaxAmount:        maxAmountMloki,
		Expiry:           expirationSecs,
		IdentityEvent:    mustMarshal(t, buildCircleWalletIdentityEvent(t, requesterPrivkey, hubWalletPubkey)),
	}
	return joinRequestJSON(t, params)
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

func TestHandleCreateCircleWalletEvent_NotCircleHub(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	standardApp, _, err := svc.AppsService.CreateApp("std", "", 0, "never", nil, []string{constants.GET_INFO_SCOPE}, db.AppKindStandard, nil, "", nil)
	require.NoError(t, err)

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *standardApp.WalletPubkey, 50_000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, ctx, nip47Request, dbRequestEvent.ID, standardApp, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_NOT_IMPLEMENTED, publishedResponse.Error.Code)
}

func TestHandleCreateCircleWalletEvent_Unauthorized(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000)

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, 50_000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: false}).
		testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
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
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, 50_000, 7200)), nip47Request) // 7200 > 3600
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	assert.NotNil(t, publishedResponse.Error)
	assert.Equal(t, nipcw.ErrLimitExceeded, publishedResponse.Error.Code)
}

// fillCircle brings provider's holdings to its holdings ceiling: a member
// wallet holding exactly the ceiling.
func fillCircle(t *testing.T, svc *tests.TestService, provider *db.App, ceilingMloki int64) {
	t.Helper()
	require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", provider.ID).
		Update("holdings_ceiling_mloki", ceilingMloki).Error)
	child, _, err := svc.AppsService.CreateApp("existing-child", "", 100, "never", nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &provider.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)
	tests.FundApp(svc, child.ID, uint64(ceilingMloki), tests.RandomHex32()) //nolint:gosec // test amount
}

// NIP-CW §Processing Algorithm step 9: a join is admitted while the circle's
// holdings are below its holdings ceiling; a new wallet starts empty, so the
// requested caps do not enter into it.
func TestHandleCreateCircleWalletEvent_HoldingsCeiling(t *testing.T) {
	join := func(svc *tests.TestService, provider *db.App) *models.Response {
		nip47Request := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, 100_000, 3600)), nip47Request))
		ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
		require.NoError(t, svc.DB.Create(&ev).Error)
		var resp *models.Response
		NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
			testJoin(t, context.TODO(), nip47Request, ev.ID, provider, func(r *models.Response, _ nostr.Tags) { resp = r })
		return resp
	}

	t.Run("at_the_ceiling_refused", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()
		provider := createCircleHub(t, svc, 7200, 0)
		fillCircle(t, svc, provider, 100_000)
		resp := join(svc, provider)
		require.NotNil(t, resp.Error)
		assert.Equal(t, nipcw.ErrInsufficientCapacity, resp.Error.Code)
	})

	t.Run("below_the_ceiling_admitted", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()
		provider := createCircleHub(t, svc, 7200, 0)
		fillCircle(t, svc, provider, 100_000)
		require.NoError(t, svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", provider.ID).
			Update("holdings_ceiling_mloki", 100_001).Error)
		resp := join(svc, provider)
		assert.Nil(t, resp.Error, "%+v", resp.Error)
		assert.Nil(t, join(svc, provider).Error, "joins add nothing to the holdings")
	})
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
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, 20_000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	controller.testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
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
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, 100_000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	assert.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(createCircleWalletResponse)
	assert.NotEmpty(t, result.EncryptedDetails)
	details := decryptCircleWalletDetails(t, requesterKey, *provider.WalletPubkey, result.EncryptedDetails)
	assert.NotEmpty(t, details.Relays)
	assert.NotEmpty(t, details.WalletPubkey)
	assert.Greater(t, details.ExpiresAt, time.Now().Unix())
	assert.Zero(t, details.FeesPpm)

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
	err = json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, 100_000, 3600)), nip47Request)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(_ *models.Response, _ nostr.Tags) {})

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

// TestHandleCreateCircleWalletEvent_NonRoundMloki_CapRoundsDownSafely: the send
// cap is stored in whole loki, so a max_amount that is not a multiple of 1000
// mloki is applied rounded DOWN, never up, and the details report what was
// applied (NIP-CW §Response); the receive cap is kept exactly as asked, and an
// invoice past it is refused.
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
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, requestedMaxAmount, 3600)), createReq))
	var params map[string]any
	require.NoError(t, json.Unmarshal(createReq.Params, &params))
	params["max_receive"] = requestedMaxAmount
	createReq.Params, err = json.Marshal(params)
	require.NoError(t, err)

	dbRequestEvent := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(&dbRequestEvent).Error)

	controller := NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true})
	var createResp *models.Response
	controller.testJoin(t, ctx, createReq, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
		createResp = r
	})
	require.Nil(t, createResp.Error)
	details := decryptCircleWalletDetails(t, requesterKey, *provider.WalletPubkey, createResp.Result.(createCircleWalletResponse).EncryptedDetails)
	assert.Equal(t, effectiveCapMloki, details.MaxAmount, "the send cap as applied, rounded down")
	assert.Equal(t, requestedMaxAmount, details.MaxReceive, "the receive cap as asked")

	var child db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).First(&child).Error)

	invoice := func(amount uint64) *models.Response {
		req := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(makeInvoiceRequestJSON(amount)), req))
		ev := &db.RequestEvent{AppId: &child.ID, NostrId: tests.RandomHex32()}
		require.NoError(t, svc.DB.Create(&ev).Error)
		var resp *models.Response
		controller.HandleMakeInvoiceEvent(ctx, req, ev.ID, child.ID, func(r *models.Response, _ nostr.Tags) { resp = r })
		return resp
	}
	assert.Nil(t, invoice(requestedMaxAmount).Error, "an invoice for the whole receive cap")
	tooMuch := invoice(1)
	require.NotNil(t, tooMuch.Error)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, tooMuch.Error.Code)
}

// makeCircleWalletRequestWithRenewal is like makeCircleWalletRequest but also
// sets budget_renewal — omit it (pass "") to test the caller-omitted default.
func makeCircleWalletRequestWithRenewal(t *testing.T, requesterPrivkey, hubWalletPubkey string, maxAmountMloki uint64, expirationSecs uint64, budgetRenewal string) string {
	t.Helper()
	requesterPubkey, _ := nostr.GetPublicKey(requesterPrivkey)
	params := nipcw.CreateCircleWalletRequest{
		Pubkey:           requesterPubkey,
		ConnectionPubkey: testConnectionPubkey(t, requesterPubkey),
		MaxAmount:        maxAmountMloki,
		Expiry:           expirationSecs,
		BudgetRenewal:    budgetRenewal,
		IdentityEvent:    mustMarshal(t, buildCircleWalletIdentityEvent(t, requesterPrivkey, hubWalletPubkey)),
	}
	return joinRequestJSON(t, params)
}

func TestHandleCreateCircleWalletEvent_ExpiryOmitted_DefaultsToMaxExpSecs(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 1_000_000) // max_exp_secs = 7200

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, 100_000, 0)), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	before := time.Now()
	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	require.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(createCircleWalletResponse)
	details := decryptCircleWalletDetails(t, requesterKey, *provider.WalletPubkey, result.EncryptedDetails)
	assert.InDelta(t, before.Add(7200*time.Second).Unix(), details.ExpiresAt, 5,
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
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, 100_001, 3600)), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	require.NotNil(t, publishedResponse.Error)
	assert.Equal(t, nipcw.ErrLimitExceeded, publishedResponse.Error.Code)
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
		require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequestWithRenewal(t, requesterKey, *provider.WalletPubkey, 11_000, 3600, renewal)), nip47Request))

		dbRequestEvent := &db.RequestEvent{}
		svc.DB.Create(&dbRequestEvent)

		var publishedResponse *models.Response
		NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
			testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
				publishedResponse = r
			})

		require.Nil(t, publishedResponse.Error, "renewal %q should be accepted under a monthly floor", renewal)
		result := publishedResponse.Result.(createCircleWalletResponse)
		details := decryptCircleWalletDetails(t, requesterKey, *provider.WalletPubkey, result.EncryptedDetails)
		assert.Equal(t, renewal, details.BudgetRenewal)

		var childApp db.App
		require.NoError(t, svc.DB.Where("wallet_pubkey = ?", details.WalletPubkey).First(&childApp).Error)
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
		require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequestWithRenewal(t, requesterKey, *provider.WalletPubkey, 11_000, 3600, renewal)), nip47Request))

		dbRequestEvent := &db.RequestEvent{}
		svc.DB.Create(&dbRequestEvent)

		var publishedResponse *models.Response
		NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
			testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
				publishedResponse = r
			})

		require.NotNil(t, publishedResponse.Error, "renewal %q should be rejected under a monthly floor", renewal)
		assert.Equal(t, nipcw.ErrLimitExceeded, publishedResponse.Error.Code)
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
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, 11_000, 3600)), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	require.Nil(t, publishedResponse.Error)
	result := publishedResponse.Result.(createCircleWalletResponse)
	details := decryptCircleWalletDetails(t, requesterKey, *provider.WalletPubkey, result.EncryptedDetails)
	assert.Equal(t, constants.BUDGET_RENEWAL_NEVER, details.BudgetRenewal)
}

func TestHandleCreateCircleWalletEvent_BudgetRenewal_InvalidValue_Rejected(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)

	requesterKey := nostr.GeneratePrivateKey()

	nip47Request := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequestWithRenewal(t, requesterKey, *provider.WalletPubkey, 11_000, 3600, "fortnightly")), nip47Request))

	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
			publishedResponse = r
		})

	require.NotNil(t, publishedResponse.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, publishedResponse.Error.Code)
}

// budget_renewal is OPTIONAL, but when present it is one of the five values:
// an explicit "" is refused, while leaving it out gets the Hub default.
func TestHandleCreateCircleWalletEvent_BudgetRenewal_EmptyRefused_AbsentAccepted(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	provider := createCircleHub(t, svc, 7200, 10_000_000)
	requesterKey := nostr.GeneratePrivateKey()
	controller := NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true})
	join := func(withEmptyRenewal bool) *models.Response {
		var content map[string]any
		require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *provider.WalletPubkey, 11_000, 3600)), &content))
		params := content["params"].(map[string]any)
		_, present := params["budget_renewal"]
		require.False(t, present, "the helper leaves an empty budget_renewal out")
		if withEmptyRenewal {
			params["budget_renewal"] = ""
		}
		raw, err := json.Marshal(content)
		require.NoError(t, err)
		req := &models.Request{}
		require.NoError(t, json.Unmarshal(raw, req))
		ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
		require.NoError(t, svc.DB.Create(ev).Error)
		var resp *models.Response
		controller.testJoin(t, ctx, req, ev.ID, provider, func(r *models.Response, _ nostr.Tags) { resp = r })
		require.NotNil(t, resp)
		return resp
	}

	refused := join(true)
	require.NotNil(t, refused.Error, "an explicit empty budget_renewal is not one of the five values")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, refused.Error.Code)

	accepted := join(false)
	require.Nil(t, accepted.Error, "an absent budget_renewal asks for the Hub default")
	details := decryptCircleWalletDetails(t, requesterKey, *provider.WalletPubkey, accepted.Result.(createCircleWalletResponse).EncryptedDetails)
	assert.Equal(t, constants.BUDGET_RENEWAL_NEVER, details.BudgetRenewal)
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
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequestWithRenewal(t, requesterKey, *provider.WalletPubkey, 11_000, 3600, constants.BUDGET_RENEWAL_YEARLY)), nip47Request))
	dbRequestEvent := &db.RequestEvent{}
	svc.DB.Create(&dbRequestEvent)

	var publishedResponse *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, ctx, nip47Request, dbRequestEvent.ID, provider, func(r *models.Response, _ nostr.Tags) {
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
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(provider.ID, apps.CircleHubConfigPatch{MinBudgetRenewal: &monthly}))

	// The existing child's own renewal must be untouched: there is no
	// re-validation after creation, only at creation time.
	var permAfter db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", child.ID, constants.PAY_INVOICE_SCOPE).First(&permAfter).Error)
	assert.Equal(t, constants.BUDGET_RENEWAL_YEARLY, permAfter.BudgetRenewal,
		"raising the hub's min_budget_renewal floor must not retroactively change an existing child's own renewal")
}
