package api

// F7 — fixed; regression test below. ReplaceCircleAllowlist now rejects any
// pubkey that isn't 64-char lowercase hex, instead of silently storing an
// invalid (non-hex, wrong-length) value that could cause auth failures or
// noise downstream.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/service"
	"github.com/flokiorg/lokihub/tests"
)

func TestReplaceCircleAllowlist_InvalidPubkeys_AreRejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}

	cases := []struct {
		name   string
		pubkey string
	}{
		{"not hex", "not-a-valid-hex-pubkey!"},
		{"too short", "deadbeef"},
		{"too long", "aa" + tests.RandomHex32() + "bb"},
		{"uppercase hex", "AAAA" + tests.RandomHex32()[4:]},
		{"npub", "npub1sg6plzptd64u62a878hep2kev88swjh3tw00gjsfl8f237lmu63q0uf63m"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := theAPI.ReplaceCircleAllowlist(app, []string{tc.pubkey})
			assert.Error(t, err,
				"pubkey %q must be rejected as invalid; got nil error instead", tc.pubkey)
		})
	}
}

func TestReplaceCircleAllowlist_ValidPubkeys_AreAccepted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)
	cfg, err := svc.AppsService.GetCircleHubConfig(app.ID)
	require.NoError(t, err)

	pk1 := tests.RandomHex32()
	pk2 := tests.RandomHex32()

	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	err = theAPI.ReplaceCircleAllowlist(app, []string{pk1, pk2})
	require.NoError(t, err, "valid 64-char hex pubkeys must be accepted")

	var rows []db.CircleIdentityAllowedPubkey
	require.NoError(t, svc.DB.Where("circle_identity_id = ?", cfg.CircleIdentityID).Find(&rows).Error)
	require.Len(t, rows, 2)
	pks := map[string]bool{rows[0].Pubkey: true, rows[1].Pubkey: true}
	assert.True(t, pks[pk1])
	assert.True(t, pks[pk2])
}

// Pasted entries are trimmed, and a pubkey listed twice is one member: it is
// admitted once and removing it once removes it entirely.
func TestReplaceCircleAllowlist_TrimmedAndDuplicatesHarmless(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	app := createTestCircleHub(t, svc, db.CirclePolicyAllowlist)
	cfg, err := svc.AppsService.GetCircleHubConfig(app.ID)
	require.NoError(t, err)
	theAPI := &api{db: svc.DB, appsSvc: svc.AppsService}
	cache := service.NewNostrSocialCache(svc.Cfg)
	isMember := func(pk string) bool {
		ok, err := cache.IsAuthorized(context.Background(), pk, &cfg.CircleIdentity, svc.DB)
		require.NoError(t, err)
		return ok
	}

	pk := tests.RandomHex32()
	require.NoError(t, theAPI.ReplaceCircleAllowlist(app, []string{"  " + pk + "\n", pk, ""}))
	assert.True(t, isMember(pk))
	assert.False(t, isMember(" "+pk), "the lookup is exact; only stored values are trimmed")

	require.NoError(t, theAPI.RemoveCircleAllowedPubkey(app, pk))
	assert.False(t, isMember(pk), "removing a duplicated member removes every copy")
}
