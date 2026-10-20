package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// allowlisted mirrors the membership check's own query (nostrSocialCache.isInAllowlist).
func allowlisted(t *testing.T, svc *tests.TestService, identityID uint, pubkey string) bool {
	t.Helper()
	var n int64
	require.NoError(t, svc.DB.Model(&db.CircleIdentityAllowedPubkey{}).
		Where("circle_identity_id = ? AND pubkey = ?", identityID, pubkey).Count(&n).Error)
	return n > 0
}

// A pubkey pasted with surrounding whitespace is stored trimmed, so its owner
// is admitted; a pubkey listed twice is harmless, and removing it once removes
// it fully, so its owner is not left a member by the second row.
func TestCircleAllowlist_WhitespaceAndDuplicates(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)
	cfg, err := svc.AppsService.GetCircleHubConfig(app.ID)
	require.NoError(t, err)
	padded, twice := tests.RandomHex32(), tests.RandomHex32()

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	require.NoError(t, theAPI.ReplaceCircleAllowlist(app, []string{"  " + padded + "\n", twice, twice, "   "}))

	assert.True(t, allowlisted(t, svc, cfg.CircleIdentityID, padded), "a padded pubkey is stored trimmed")
	assert.True(t, allowlisted(t, svc, cfg.CircleIdentityID, twice))
	listed, err := theAPI.ListCircleAllowlist(app)
	require.NoError(t, err)
	assert.Contains(t, listed, padded)

	require.NoError(t, theAPI.RemoveCircleAllowedPubkey(app, twice))
	assert.False(t, allowlisted(t, svc, cfg.CircleIdentityID, twice), "one removal drops every copy")
	assert.True(t, allowlisted(t, svc, cfg.CircleIdentityID, padded))
}
