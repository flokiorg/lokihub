package api

// F7 — ReplaceCircleAllowlist stores pubkeys without any format validation.
// An invalid (non-hex, wrong-length) pubkey silently enters the DB allowlist,
// potentially causing auth failures or noise downstream.
//
// Correct behaviour: pubkeys that are not 64-char lowercase hex must be
// rejected with an error.  This test FAILS today because the function stores
// any non-empty string.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
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
