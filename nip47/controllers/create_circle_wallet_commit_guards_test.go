package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// slowSocialCache authorizes every requester after a delay, so a join spends
// that long between its proof check and its commit.
type slowSocialCache struct{ delay time.Duration }

func (m *slowSocialCache) IsAuthorized(_ context.Context, _ string, _ *db.CircleIdentity, _ *gorm.DB) (bool, error) {
	time.Sleep(m.delay)
	return true, nil
}

// joinProofAt is a join request whose proof is dated createdAt.
func joinProofAt(t *testing.T, requesterKey, hubPubkey string, createdAt nostr.Timestamp, expiry uint64) *models.Request {
	t.Helper()
	requesterPubkey := mustPubkeyForTest(t, requesterKey)
	ev := &nostr.Event{
		Kind:      nostrKindCircleIdentityProof,
		CreatedAt: createdAt,
		Tags:      nostr.Tags{{"d", hubPubkey}, {"connection", testConnectionPubkey(t, requesterPubkey)}},
	}
	require.NoError(t, ev.Sign(requesterKey))
	joinProofSigners.Store(ev.ID, requesterKey)
	req := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(joinRequestJSON(t, nipcw.CreateCircleWalletRequest{
		Pubkey: requesterPubkey, ConnectionPubkey: testConnectionPubkey(t, requesterPubkey),
		MaxAmount: 50_000, Expiry: expiry, IdentityEvent: mustMarshal(t, ev),
	})), req))
	return req
}

// NIP-CW §Processing Algorithm, step 9: a join whose proof leaves its freshness
// window before the commit is refused with BAD_REQUEST and makes no wallet.
func TestCreateCircleWallet_PastFreshnessAtCommit_BadRequestNothingMade(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)

	// Fresh at step 2 (299 s old), 301+ s old by the commit.
	req := joinProofAt(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, nostr.Timestamp(time.Now().Unix()-299), 3600)
	var resp *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &slowSocialCache{delay: 2500 * time.Millisecond}).
		testJoin(t, context.TODO(), req, 0, provider, func(r *models.Response, _ nostr.Tags) { resp = r })

	require.NotNil(t, resp)
	require.NotNil(t, resp.Error, "a join committed past its proof's deadline")
	assert.Equal(t, nipcw.ErrBadRequest, resp.Error.Code)
	var wallets, records int64
	svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Count(&wallets)
	svc.DB.Model(&db.CircleJoinRecord{}).Where("circle_hub_app_id = ?", provider.ID).Count(&records)
	assert.Zero(t, wallets)
	assert.Zero(t, records)

	// The same delay on a proof made now commits inside its window.
	resp = nil
	NewTestNip47ControllerWithSocialCache(svc, &slowSocialCache{delay: 2500 * time.Millisecond}).
		testJoin(t, context.TODO(), joinProofAt(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, nostr.Now(), 3600), 0, provider,
			func(r *models.Response, _ nostr.Tags) { resp = r })
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)
}

// NIP-CW §Processing Algorithm, step 6: an expiry exactly at the Hub's ceiling
// is accepted and one second above it is refused with LIMIT_EXCEEDED.
func TestCreateCircleWallet_ExpiryAtCeilingAcceptedOneAboveRefused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 3600, 1_000_000)
	controller := NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true})

	var resp *models.Response
	controller.testJoin(t, context.TODO(), joinProofAt(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, nostr.Now(), 3601), 0, provider,
		func(r *models.Response, _ nostr.Tags) { resp = r })
	require.NotNil(t, resp.Error)
	assert.Equal(t, nipcw.ErrLimitExceeded, resp.Error.Code)

	resp = nil
	controller.testJoin(t, context.TODO(), joinProofAt(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, nostr.Now(), 3600), 0, provider,
		func(r *models.Response, _ nostr.Tags) { resp = r })
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)
}

// NIP-CW §Data Model: a join proof's id stays in the single-use record until at
// least its created_at plus 6 minutes, even when the join is refused after step 3.
func TestCreateCircleWallet_ReplayRecordKeptSixMinutes(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)

	createdAt := time.Now().Unix() - 10
	req := joinProofAt(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, nostr.Timestamp(createdAt), 3600)
	var resp *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: false}).
		testJoin(t, context.TODO(), req, 0, provider, func(r *models.Response, _ nostr.Tags) { resp = r })
	require.NotNil(t, resp.Error)
	assert.Equal(t, nipcw.ErrRestricted, resp.Error.Code)

	var proof db.CircleWalletIdentityProof
	require.NoError(t, svc.DB.Where("app_id = ?", provider.ID).First(&proof).Error)
	assert.Equal(t, db.CircleProofJoin, proof.Kind)
	assert.GreaterOrEqual(t, proof.RetainUntil, createdAt+360)
}

// NIP-CW §Processing Algorithm, step 9: the membership row's uniqueness is the
// authoritative one-wallet guard. A rival wallet for the same member committed
// after the step-5 pre-check makes the join roll back whole with WALLET_EXISTS.
func TestCreateCircleWallet_MembershipConflictInTx_RollsBackWalletExists(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)
	requesterKey := nostr.GeneratePrivateKey()
	requesterPub := mustPubkeyForTest(t, requesterKey)

	rival := &db.App{Name: "rival", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleWallet, ParentAppID: &provider.ID, ParentKind: db.ParentKindCircle}
	require.NoError(t, svc.DB.Create(rival).Error)
	armed := true
	require.NoError(t, svc.DB.Callback().Create().After("gorm:create").Register("test:rival_membership", func(d *gorm.DB) {
		if !armed || d.Statement.Table != "apps" {
			return
		}
		armed = false
		require.NoError(t, d.Session(&gorm.Session{NewDB: true}).Create(&db.CircleWalletMembership{
			CircleHubAppID: provider.ID, RequesterPubkey: requesterPub, WalletAppID: rival.ID}).Error)
	}))

	var resp *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}).
		testJoin(t, context.TODO(), joinProofAt(t, requesterKey, *provider.WalletPubkey, nostr.Now(), 3600), 0, provider,
			func(r *models.Response, _ nostr.Tags) { resp = r })
	require.False(t, armed, "the rival membership was never inserted")
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error)
	assert.Equal(t, nipcw.ErrWalletExists, resp.Error.Code)
	var wallets, records int64
	svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Count(&wallets)
	svc.DB.Model(&db.CircleJoinRecord{}).Where("circle_hub_app_id = ?", provider.ID).Count(&records)
	assert.EqualValues(t, 1, wallets, "only the rival wallet")
	assert.Zero(t, records)
}

// NIP-CW §Data Model: once a member's wallet is past its expiry it is neither
// Active nor Suspended, so the member may join again even before the lifecycle
// job has recorded the expiry.
func TestCreateCircleWallet_ExpiredWalletFreesTheMember(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)
	controller := NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true})
	requesterKey := nostr.GeneratePrivateKey()
	requesterPub := mustPubkeyForTest(t, requesterKey)

	var resp *models.Response
	controller.testJoin(t, context.TODO(), joinProofAt(t, requesterKey, *provider.WalletPubkey, nostr.Now(), 1), 0, provider,
		func(r *models.Response, _ nostr.Tags) { resp = r })
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
	time.Sleep(2100 * time.Millisecond)

	// A second join needs another connection key: the first is still authorized.
	ev := &nostr.Event{Kind: nostrKindCircleIdentityProof, CreatedAt: nostr.Now(),
		Tags: nostr.Tags{{"d", *provider.WalletPubkey}, {"connection", mustPubkeyForTest(t, nostr.GeneratePrivateKey())}}}
	connection := ev.Tags[1][1]
	require.NoError(t, ev.Sign(requesterKey))
	joinProofSigners.Store(ev.ID, requesterKey)
	req := &models.Request{}
	require.NoError(t, json.Unmarshal([]byte(joinRequestJSON(t, nipcw.CreateCircleWalletRequest{
		Pubkey: requesterPub, ConnectionPubkey: connection, MaxAmount: 50_000, Expiry: 3600, IdentityEvent: mustMarshal(t, ev),
	})), req))
	resp = nil
	controller.testJoin(t, context.TODO(), req, 0, provider, func(r *models.Response, _ nostr.Tags) { resp = r })
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error, "an expired wallet still blocks its member: %+v", resp.Error)
}
