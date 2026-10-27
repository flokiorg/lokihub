package controllers

// Circle wallet behaviour that is correct today and must stay that way: a
// member's wallet can't mint wallets of its own, and a hub owner changing the
// hub's limits or fee rate affects new members only.

import (
	"context"
	"encoding/json"
	"github.com/flokiorg/lokihub/apps"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func joinedWallet(t *testing.T, svc *tests.TestService, hub *db.App, requesterKey string) *db.App {
	t.Helper()
	pub, err := nostr.GetPublicKey(requesterKey)
	require.NoError(t, err)
	var m db.CircleWalletMembership
	require.NoError(t, svc.DB.Where("circle_hub_app_id = ? AND requester_pubkey = ?", hub.ID, pub).First(&m).Error)
	var wallet db.App
	require.NoError(t, svc.DB.First(&wallet, m.WalletAppID).Error)
	return &wallet
}

func joinWithExpiry(t *testing.T, svc *tests.TestService, hub *db.App, requesterKey string, maxAmountMloki uint64, expirySecs int) *models.Response {
	t.Helper()
	r := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, *hub.WalletPubkey, maxAmountMloki, expirySecs)), r))
	ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(ev).Error)
	var resp *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, context.TODO(), r, ev.ID, hub, func(rr *models.Response, _ nostr.Tags) { resp = rr })
	require.NotNil(t, resp)
	return resp
}

func TestCircleWallet_CannotCreateCircleWallets(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := createCircleHub(t, svc, 7200, 1_000_000)
	member := nostr.GeneratePrivateKey()
	require.Nil(t, joinWithExpiry(t, svc, hub, member, 100_000, 3600).Error)
	wallet := joinedWallet(t, svc, hub, member)

	// Even with a valid request aimed at its own wallet key, a member's wallet
	// is not a hub.
	resp := joinWithExpiry(t, svc, wallet, nostr.GeneratePrivateKey(), 1_000, 3600)
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_NOT_IMPLEMENTED, resp.Error.Code)
	var grandchildren int64
	svc.DB.Model(&db.App{}).Where("parent_app_id = ?", wallet.ID).Count(&grandchildren)
	assert.Zero(t, grandchildren)
}

func TestCircleHub_LoweringPerWalletCap_AppliesToNewMembersOnly(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := createCircleHubWithCaps(t, svc, 7200, 10_000_000, 500_000, "")
	early := nostr.GeneratePrivateKey()
	require.Nil(t, joinWithExpiry(t, svc, hub, early, 400_000, 3600).Error)

	lowered := 100_000
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(hub.ID, apps.CircleHubConfigPatch{PerWalletMaxMloki: &lowered}))

	var perm db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", joinedWallet(t, svc, hub, early).ID, constants.MAKE_INVOICE_SCOPE).First(&perm).Error)
	assert.Equal(t, 400, perm.MaxAmountLoki, "an existing member keeps the cap they joined with")

	over := joinWithExpiry(t, svc, hub, nostr.GeneratePrivateKey(), 200_000, 3600)
	require.NotNil(t, over.Error)
	assert.Equal(t, nipcw.ErrLimitExceeded, over.Error.Code, "a new member is held to the lowered cap")
	assert.Nil(t, joinWithExpiry(t, svc, hub, nostr.GeneratePrivateKey(), 100_000, 3600).Error)
}

func TestCircleHub_LoweringMaxExpiry_AppliesToNewMembersOnly(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := createCircleHub(t, svc, 7200, 10_000_000)
	early := nostr.GeneratePrivateKey()
	require.Nil(t, joinWithExpiry(t, svc, hub, early, 100_000, 7000).Error)
	before := joinedWallet(t, svc, hub, early).ExpiresAt
	require.NotNil(t, before)

	lowered := 3600
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(hub.ID, apps.CircleHubConfigPatch{MaxExpSecs: &lowered}))

	after := joinedWallet(t, svc, hub, early).ExpiresAt
	require.NotNil(t, after)
	assert.WithinDuration(t, *before, *after, time.Second, "an existing member's wallet keeps its expiry")
	assert.True(t, after.After(time.Now().Add(time.Hour)))

	tooLong := joinWithExpiry(t, svc, hub, nostr.GeneratePrivateKey(), 100_000, 7000)
	require.NotNil(t, tooLong.Error)
	assert.Equal(t, nipcw.ErrLimitExceeded, tooLong.Error.Code, "a new member is held to the lowered max expiry")
}

// NIP-CW §Fees for the zero case: a member who joined a fee-free hub stays
// fee-free when the hub starts charging. A NULL rate would fall back to the
// hub's current one, so the join must store an explicit 0.
func TestCircleWallet_JoinedFeeFree_StaysFeeFreeWhenHubStartsCharging(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := createCircleHub(t, svc, 7200, 10_000_000) // 0 ppm
	member := nostr.GeneratePrivateKey()
	require.Nil(t, joinWithExpiry(t, svc, hub, member, 500_000, 3600).Error)
	wallet := joinedWallet(t, svc, hub, member)
	require.NotNil(t, wallet.CircleFeesPpm, "the join stores the rate, even when it is zero")
	assert.Equal(t, 0, *wallet.CircleFeesPpm)

	raised := 50_000
	require.NoError(t, svc.AppsService.UpdateCircleHubConfig(hub.ID, apps.CircleHubConfigPatch{FeesPpm: &raised}))
	tests.FundApp(svc, wallet.ID, 200_000, tests.RandomHex32())

	r := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(nip47PayInvoiceJson), r))
	ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
	require.NoError(t, svc.DB.Create(ev).Error)
	var published *models.Response
	NewTestNip47Controller(svc).HandlePayInvoiceEvent(context.TODO(), r, ev.ID, wallet,
		func(rr *models.Response, _ nostr.Tags) { published = rr }, nostr.Tags{})
	require.NotNil(t, published)
	require.Nil(t, published.Error, "%+v", published.Error)
	assert.Zero(t, published.Result.(payResponse).FeeSkimMloki)
}
