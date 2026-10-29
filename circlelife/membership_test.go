package circlelife_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CW §Membership: a member named again before their wallet's grace ends gets
// it back Active; named again once the grace has ended, the wallet stays
// Suspended and the resolution still applies to everyone else.
func TestApplyResolution_ResumesOnlyBeforeGraceEnds(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	const grace = int64(1000)
	s := time.Unix(1_800_000_000, 0)

	identity := db.CircleIdentity{Name: "c", Policy: db.CirclePolicyAllowlist}
	require.NoError(t, svc.DB.Create(&identity).Error)
	wallet := func() (*db.App, string) {
		w := newWallet(t, svc, nil)
		require.NoError(t, svc.DB.Create(&db.CircleHubConfig{AppID: *w.ParentAppID, CircleIdentityID: identity.ID, GraceSecs: grace}).Error)
		var m db.CircleWalletMembership
		require.NoError(t, svc.DB.Where("wallet_app_id = ?", w.ID).First(&m).Error)
		require.NoError(t, circlelife.Transition(svc.DB, w, circlelife.EventSuspend, s, grace))
		return w, m.RequesterPubkey
	}
	late, latePub := wallet()
	early, earlyPub := wallet()
	named := func(pk string) bool { return pk == latePub || pk == earlyPub }

	// At S+grace the late wallet's grace has ended; resolve it alone first.
	_, err = circlelife.ApplyResolution(svc.DB, identity.ID, func(pk string) bool { return pk == latePub }, s.Add(time.Duration(grace)*time.Second))
	require.NoError(t, err, "a past-grace member named again must not fail the resolution")
	var stored db.App
	require.NoError(t, svc.DB.First(&stored, late.ID).Error)
	assert.Equal(t, circlelife.StateSuspended, stored.CircleState)

	_, err = circlelife.ApplyResolution(svc.DB, identity.ID, named, s.Add(time.Duration(grace-1)*time.Second))
	require.NoError(t, err)
	var resumed db.App
	require.NoError(t, svc.DB.First(&resumed, early.ID).Error)
	assert.Equal(t, circlelife.StateActive, resumed.CircleState)
}
