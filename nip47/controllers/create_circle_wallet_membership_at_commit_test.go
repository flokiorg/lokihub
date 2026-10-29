package controllers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// hookSocialCache reads the allowlist through the db it is handed, after
// running hook on its first call: hook stands for whatever commits between a
// join's membership check (step 4) and its transaction.
type hookSocialCache struct {
	hook func()
	once sync.Once
}

func (m *hookSocialCache) IsAuthorized(_ context.Context, pubkey string, identity *db.CircleIdentity, gormDB *gorm.DB) (bool, error) {
	var n int64
	err := gormDB.Model(&db.CircleIdentityAllowedPubkey{}).Where("circle_identity_id = ? AND pubkey = ?", identity.ID, pubkey).
		Count(&n).Error
	m.once.Do(m.hook)
	return n > 0, err
}

// NIP-CW §Membership: on an allowlist Hub each committed edit is a successful
// resolution applied at once, so a member removed while their join is in
// flight must not end up holding an Active wallet.
func TestCreateCircleWallet_AllowlistRemovalDuringJoin_NoActiveWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)
	cfg, err := svc.AppsService.GetCircleHubConfig(provider.ID)
	require.NoError(t, err)

	requesterKey := nostr.GeneratePrivateKey()
	requesterPub := mustPubkeyForTest(t, requesterKey)
	require.NoError(t, svc.DB.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: cfg.CircleIdentityID, Pubkey: requesterPub}).Error)

	// The host removes the member after step 4 passed, before step 9 commits.
	removal := func() {
		require.NoError(t, svc.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("circle_identity_id = ?", cfg.CircleIdentityID).Delete(&db.CircleIdentityAllowedPubkey{}).Error; err != nil {
				return err
			}
			_, err := circlelife.ApplyResolution(tx, cfg.CircleIdentityID, func(string) bool { return false }, time.Now())
			return err
		}))
	}
	var resp *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &hookSocialCache{hook: removal}).
		testJoin(t, context.TODO(), joinProofAt(t, requesterKey, *provider.WalletPubkey, nostr.Now(), 3600), 0, provider,
			func(r *models.Response, _ nostr.Tags) { resp = r })
	require.NotNil(t, resp)

	require.NotNil(t, resp.Error)
	assert.Equal(t, nipcw.ErrRestricted, resp.Error.Code)
	var wallets []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Find(&wallets)
	for _, w := range wallets {
		assert.NotEqual(t, circlelife.StateActive, circlelife.EffectiveState(&w, time.Now()),
			"a member removed by a committed allowlist edit holds an Active wallet (join answered %+v)", resp.Error)
	}

	// A member still named when the join commits gets an Active wallet.
	otherKey := nostr.GeneratePrivateKey()
	require.NoError(t, svc.DB.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: cfg.CircleIdentityID, Pubkey: mustPubkeyForTest(t, otherKey)}).Error)
	resp = nil
	NewTestNip47ControllerWithSocialCache(svc, &hookSocialCache{hook: func() {}}).
		testJoin(t, context.TODO(), joinProofAt(t, otherKey, *provider.WalletPubkey, nostr.Now(), 3600), 0, provider,
			func(r *models.Response, _ nostr.Tags) { resp = r })
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
	svc.DB.Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Find(&wallets)
	require.Len(t, wallets, 1)
	assert.Equal(t, circlelife.StateActive, circlelife.EffectiveState(&wallets[0], time.Now()))
}

// secondCallFails authorizes on its first call and fails every later one.
type secondCallFails struct{ calls int }

func (m *secondCallFails) IsAuthorized(context.Context, string, *db.CircleIdentity, *gorm.DB) (bool, error) {
	m.calls++
	if m.calls > 1 {
		return false, constants.ErrCircleMembershipUnresolved
	}
	return true, nil
}

// NIP-CW §Processing Algorithm, step 4: membership that cannot be read again at
// the commit is NOT_READY, and nothing is made.
func TestCreateCircleWallet_MembershipUnreadableAtCommit_NotReadyNothingMade(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 1_000_000)
	var resp *models.Response
	NewTestNip47ControllerWithSocialCache(svc, &secondCallFails{}).
		testJoin(t, context.TODO(), joinProofAt(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, nostr.Now(), 3600), 0, provider,
			func(r *models.Response, _ nostr.Tags) { resp = r })
	require.NotNil(t, resp)
	require.NotNil(t, resp.Error)
	assert.Equal(t, nipcw.ErrNotReady, resp.Error.Code)
	var wallets int64
	svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Count(&wallets)
	assert.Zero(t, wallets)
}
