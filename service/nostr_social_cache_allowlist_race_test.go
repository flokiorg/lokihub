package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// replaceAllowlistForTest mirrors api.ReplaceCircleAllowlist's whole-set
// delete-and-reinsert transaction (service can't import the api package —
// api imports service — so this is a minimal standalone equivalent) purely
// to drive concurrent writes against the same read path
// (isInAllowlist/IsAuthorized) this test exercises.
func replaceAllowlistForTest(t *testing.T, gormDB *gorm.DB, identityID uint, pubkeys []string) {
	t.Helper()
	require.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("circle_identity_id = ?", identityID).Delete(&db.CircleIdentityAllowedPubkey{}).Error; err != nil {
			return err
		}
		for _, pk := range pubkeys {
			if err := tx.Create(&db.CircleIdentityAllowedPubkey{CircleIdentityID: identityID, Pubkey: pk}).Error; err != nil {
				return err
			}
		}
		return nil
	}))
}

// A hub owner editing the allowlist (add/remove a pubkey) concurrently with
// an in-flight create_circle_wallet authorization check for that same pubkey
// must never corrupt reads (query errors/panics) — isInAllowlist is a plain
// COUNT(*) with no caching or in-memory state, so each read should simply
// observe either the before- or after-state of any given concurrent write,
// never an inconsistent/erroring one.
func TestIsAuthorized_Allowlist_ConcurrentReplaceDoesNotCorruptReads(t *testing.T) {
	cache, svc := newSocialCacheForTest(t)
	defer svc.Remove()

	app, _, err := svc.AppsService.CreateCircleHub("circle", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, nil,
		apps.CircleIdentityRef{Name: "circle", Policy: db.CirclePolicyAllowlist},
		db.CircleHubConfig{MaxExpSecs: 3600, PerWalletMaxMloki: 100_000},
	)
	require.NoError(t, err)
	cfg, err := svc.AppsService.GetCircleHubConfig(app.ID)
	require.NoError(t, err)

	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer: repeatedly toggles requesterPubkey on and off the allowlist.
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if i%2 == 0 {
				replaceAllowlistForTest(t, svc.DB, cfg.CircleIdentityID, []string{requesterPubkey})
			} else {
				replaceAllowlistForTest(t, svc.DB, cfg.CircleIdentityID, nil)
			}
		}
	}()

	// Reader: repeatedly checks authorization for the same pubkey — every
	// call must return cleanly (no error), regardless of which side of a
	// concurrent write it lands on.
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, err := cache.IsAuthorized(context.TODO(), requesterPubkey, &cfg.CircleIdentity, svc.DB)
			assert.NoError(t, err)
		}
	}()

	wg.Wait()

	// Final state is deterministic: the writer's last iteration (i=49, odd)
	// clears the allowlist, so the identity must end up unauthorized.
	ok, err := cache.IsAuthorized(context.TODO(), requesterPubkey, &cfg.CircleIdentity, svc.DB)
	require.NoError(t, err)
	assert.False(t, ok, "after the writer's final clear, the identity must read as unauthorized")
}
