package migrations

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

func newMigratedDB(t *testing.T) *gorm.DB {
	t.Helper()
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: filepath.Join(t.TempDir(), "drop_following_circles_test.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })
	require.NoError(t, Migrate(gormDB))
	return gormDB
}

// seedCircle creates one identity, one circle hub using it and one circle
// wallet under that hub with a membership row, and returns their ids.
func seedCircle(t *testing.T, gormDB *gorm.DB, policy, suffix string) (identityID, hubID, walletID uint) {
	t.Helper()
	identity := db.CircleIdentity{Name: "identity " + suffix, Policy: policy, ProviderPubkey: "provider" + suffix}
	require.NoError(t, gormDB.Create(&identity).Error)
	if policy == db.CirclePolicyAllowlist {
		require.NoError(t, gormDB.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: identity.ID, Pubkey: "member" + suffix}).Error)
	}

	hub := db.App{Name: "hub " + suffix, AppPubkey: "hub" + suffix, Kind: db.AppKindCircleHub}
	require.NoError(t, gormDB.Create(&hub).Error)
	require.NoError(t, gormDB.Create(&db.CircleHubConfig{AppID: hub.ID, CircleIdentityID: identity.ID}).Error)

	wallet := db.App{Name: "wallet " + suffix, AppPubkey: "wallet" + suffix, Kind: db.AppKindCircleWallet,
		ParentAppID: &hub.ID, ParentKind: db.ParentKindCircle}
	require.NoError(t, gormDB.Create(&wallet).Error)
	require.NoError(t, gormDB.Create(&db.AppPermission{AppId: wallet.ID, Scope: "pay_invoice"}).Error)
	require.NoError(t, gormDB.Create(&db.CircleWalletMembership{CircleHubAppID: hub.ID, RequesterPubkey: "member" + suffix, WalletAppID: wallet.ID}).Error)
	return identity.ID, hub.ID, wallet.ID
}

func countWhere(t *testing.T, gormDB *gorm.DB, model interface{}, query string, args ...interface{}) int64 {
	t.Helper()
	var count int64
	require.NoError(t, gormDB.Model(model).Where(query, args...).Count(&count).Error)
	return count
}

func TestMigrateDropFollowingCircles(t *testing.T) {
	gormDB := newMigratedDB(t)
	// The "following" value is what a pre-follow_set database holds.
	followingIdentity, followingHub, followingWallet := seedCircle(t, gormDB, legacyCirclePolicyFollowing, "a")
	allowlistIdentity, allowlistHub, allowlistWallet := seedCircle(t, gormDB, db.CirclePolicyAllowlist, "b")

	require.NoError(t, MigrateDropFollowingCircles(gormDB))
	require.NoError(t, MigrateDropFollowingCircles(gormDB), "a second run is a no-op")

	require.Zero(t, countWhere(t, gormDB, &db.CircleIdentity{}, "id = ?", followingIdentity))
	require.Zero(t, countWhere(t, gormDB, &db.App{}, "id IN ?", []uint{followingHub, followingWallet}))
	require.Zero(t, countWhere(t, gormDB, &db.CircleHubConfig{}, "app_id = ?", followingHub))
	require.Zero(t, countWhere(t, gormDB, &db.AppPermission{}, "app_id = ?", followingWallet))
	require.Zero(t, countWhere(t, gormDB, &db.CircleWalletMembership{}, "circle_hub_app_id = ?", followingHub))

	require.EqualValues(t, 1, countWhere(t, gormDB, &db.CircleIdentity{}, "id = ?", allowlistIdentity))
	require.EqualValues(t, 1, countWhere(t, gormDB, &db.CircleIdentityAllowedPubkey{}, "circle_identity_id = ?", allowlistIdentity))
	require.EqualValues(t, 2, countWhere(t, gormDB, &db.App{}, "id IN ?", []uint{allowlistHub, allowlistWallet}))
	require.EqualValues(t, 1, countWhere(t, gormDB, &db.CircleWalletMembership{}, "circle_hub_app_id = ?", allowlistHub))
}

// An identity no hub uses is still dropped.
func TestMigrateDropFollowingCircles_UnusedIdentity(t *testing.T) {
	gormDB := newMigratedDB(t)
	identity := db.CircleIdentity{Name: "unused", Policy: legacyCirclePolicyFollowing, ProviderPubkey: "p"}
	require.NoError(t, gormDB.Create(&identity).Error)

	require.NoError(t, MigrateDropFollowingCircles(gormDB))
	require.Zero(t, countWhere(t, gormDB, &db.CircleIdentity{}, "id = ?", identity.ID))
}

func TestMigrateDropFollowingCircles_FreshDB(t *testing.T) {
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: filepath.Join(t.TempDir(), "empty.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })
	require.NoError(t, MigrateDropFollowingCircles(gormDB))
}
