package circlelife_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// U-16.1: exactly the edges of NIP-CW §Lifecycle and Deletion.
func TestNext_AllowedAndForbiddenEdges(t *testing.T) {
	allowed := []struct {
		from  string
		event circlelife.Event
		to    string
	}{
		{circlelife.StateActive, circlelife.EventSuspend, circlelife.StateSuspended},
		{circlelife.StateSuspended, circlelife.EventResume, circlelife.StateActive},
		{circlelife.StateActive, circlelife.EventExpire, circlelife.StateExpired},
		{circlelife.StateSuspended, circlelife.EventExpire, circlelife.StateExpired},
		{circlelife.StateActive, circlelife.EventHostDelete, circlelife.StateClosing},
		{circlelife.StateActive, circlelife.EventTeardown, circlelife.StateClosing},
		{circlelife.StateSuspended, circlelife.EventHostDelete, circlelife.StateClosing},
		{circlelife.StateSuspended, circlelife.EventTeardown, circlelife.StateClosing},
		{circlelife.StateSuspended, circlelife.EventGraceEnd, circlelife.StateClosing},
	}
	for _, e := range allowed {
		to, err := circlelife.Next(e.from, e.event)
		require.NoError(t, err, "%s on %s", e.event, e.from)
		assert.Equal(t, e.to, to)
	}
	forbidden := []struct {
		from  string
		event circlelife.Event
	}{
		{circlelife.StateExpired, circlelife.EventResume},
		{circlelife.StateClosing, circlelife.EventResume},
		{circlelife.StateExpired, circlelife.EventHostDelete},
		{circlelife.StateExpired, circlelife.EventTeardown},
		{circlelife.StateClosing, circlelife.EventExpire},
		{circlelife.StateExpired, circlelife.EventExpire},
		{circlelife.StateActive, circlelife.EventResume},
		{circlelife.StateActive, circlelife.EventGraceEnd},
		{circlelife.StateSuspended, circlelife.EventSuspend},
		{circlelife.StateExpired, circlelife.EventSuspend},
		{circlelife.StateClosing, circlelife.EventSuspend},
		{circlelife.StateExpired, circlelife.EventGraceEnd},
	}
	for _, e := range forbidden {
		_, err := circlelife.Next(e.from, e.event)
		assert.True(t, errors.Is(err, circlelife.ErrForbiddenEdge), "%s on %s must be forbidden", e.event, e.from)
	}
}

// S-CW-131: a wallet is still Active at expires_at, and Expired one second after,
// whether or not the job has run.
func TestEffectiveState_ExpiryBoundary(t *testing.T) {
	expires := time.Unix(1_800_000_000, 0)
	w := &db.App{ExpiresAt: &expires}
	assert.Equal(t, circlelife.StateActive, circlelife.EffectiveState(w, expires))
	assert.Equal(t, circlelife.StateExpired, circlelife.EffectiveState(w, expires.Add(time.Second)))
	w.CircleState = circlelife.StateSuspended
	assert.Equal(t, circlelife.StateExpired, circlelife.EffectiveState(w, expires.Add(time.Second)))
	w.CircleState = circlelife.StateClosing
	assert.Equal(t, circlelife.StateClosing, circlelife.EffectiveState(w, expires.Add(time.Second)), "no Closing to Expired edge")
}

func newWallet(t *testing.T, svc *tests.TestService, expires *time.Time) *db.App {
	t.Helper()
	hub := &db.App{Name: "hub", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(hub).Error)
	w := &db.App{Name: "w", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleWallet, ParentAppID: &hub.ID,
		ParentKind: db.ParentKindCircle, ExpiresAt: expires, CircleState: circlelife.StateActive}
	require.NoError(t, svc.DB.Create(w).Error)
	require.NoError(t, svc.DB.Create(&db.CircleWalletMembership{CircleHubAppID: hub.ID, RequesterPubkey: tests.RandomHex32(), WalletAppID: w.ID}).Error)
	return w
}

func memberships(t *testing.T, svc *tests.TestService, w *db.App) int64 {
	t.Helper()
	var n int64
	require.NoError(t, svc.DB.Model(&db.CircleWalletMembership{}).Where("wallet_app_id = ?", w.ID).Count(&n).Error)
	return n
}

// U-16.RD2 and U-16.4: a wallet Suspended at S keeps the grace that began at S
// when it then expires or is host-deleted; the membership row goes when it
// reaches Expired or Closing, not when it is Suspended.
func TestTransition_GraceKeptAndMembershipRemoved(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	const grace = int64(1000)
	s := time.Unix(1_800_000_000, 0)

	expires := s.Add(100 * time.Second)
	w := newWallet(t, svc, &expires)
	require.NoError(t, circlelife.Transition(svc.DB, w, circlelife.EventSuspend, s, grace))
	assert.Equal(t, circlelife.StateSuspended, w.CircleState)
	require.NotNil(t, w.CircleGraceEndsAt)
	assert.Equal(t, s.Unix()+grace, *w.CircleGraceEndsAt)
	assert.EqualValues(t, 1, memberships(t, svc, w), "a Suspended wallet still counts for the one-wallet rule")

	require.NoError(t, circlelife.Transition(svc.DB, w, circlelife.EventExpire, s.Add(101*time.Second), grace))
	assert.Equal(t, circlelife.StateExpired, w.CircleState)
	assert.Equal(t, s.Unix()+grace, *w.CircleGraceEndsAt, "the grace is not restarted")
	assert.Zero(t, memberships(t, svc, w))

	other := newWallet(t, svc, nil)
	require.NoError(t, circlelife.Transition(svc.DB, other, circlelife.EventSuspend, s, grace))
	require.NoError(t, circlelife.Transition(svc.DB, other, circlelife.EventHostDelete, s.Add(100*time.Second), grace))
	assert.Equal(t, circlelife.StateClosing, other.CircleState)
	assert.Equal(t, s.Unix()+grace, *other.CircleGraceEndsAt)

	// An Active wallet expiring starts its grace at expires_at, not at the
	// moment the job saw it.
	late := newWallet(t, svc, &expires)
	require.NoError(t, circlelife.Transition(svc.DB, late, circlelife.EventExpire, expires.Add(time.Hour), grace))
	assert.Equal(t, expires.Unix()+grace, *late.CircleGraceEndsAt)
}

// A Suspended wallet returns to Active, the same wallet with its grace cleared,
// only while it is within its grace.
func TestTransition_ResumeOnlyWithinGrace(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	const grace = int64(1000)
	s := time.Unix(1_800_000_000, 0)

	w := newWallet(t, svc, nil)
	require.NoError(t, circlelife.Transition(svc.DB, w, circlelife.EventSuspend, s, grace))
	require.NoError(t, circlelife.Transition(svc.DB, w, circlelife.EventResume, s.Add(999*time.Second), grace))
	assert.Equal(t, circlelife.StateActive, w.CircleState)
	assert.Nil(t, w.CircleGraceEndsAt)

	late := newWallet(t, svc, nil)
	require.NoError(t, circlelife.Transition(svc.DB, late, circlelife.EventSuspend, s, grace))
	err = circlelife.Transition(svc.DB, late, circlelife.EventResume, s.Add(1000*time.Second), grace)
	assert.ErrorIs(t, err, circlelife.ErrForbiddenEdge, "the grace ended at S+grace")
	var stored db.App
	require.NoError(t, svc.DB.First(&stored, late.ID).Error)
	assert.Equal(t, circlelife.StateSuspended, stored.CircleState)
}
