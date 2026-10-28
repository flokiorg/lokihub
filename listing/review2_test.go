package listing

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
)

// R2-L2: a circle not accepting joins, or being torn down, is not listed open.
func TestCreate_RefusesCircleNotAcceptingJoins(t *testing.T) {
	f := newFixture(t)
	hub := f.circle(t, db.CirclePolicyAllowlist)
	no := false
	require.NoError(t, f.svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", hub.ID).Update("accepting_joins", &no).Error)
	_, err := f.s.Create(context.Background(), CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.ErrorIs(t, err, ErrRefused)

	hub2 := f.circle(t, db.CirclePolicyAllowlist)
	at := f.clock.Now().Unix()
	require.NoError(t, f.svc.DB.Model(&db.CircleHubConfig{}).Where("app_id = ?", hub2.ID).Update("teardown_requested_at", &at).Error)
	_, err = f.s.Create(context.Background(), CreateRequest{CircleHubAppID: uintp(hub2.ID)})
	require.ErrorIs(t, err, ErrRefused)
}

// R2-L1: a close that fails before its closed version is signed leaves the
// listing due now, so the scheduler retries it at once.
func TestClose_FailureDueNow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	hub := f.circle(t, db.CirclePolicyAllowlist)
	v, err := f.s.Create(ctx, CreateRequest{CircleHubAppID: uintp(hub.ID)})
	require.NoError(t, err)
	before, _ := f.s.load(v.ID)
	require.Greater(t, before.DueAt, f.clock.Now().Unix()+60)

	ks := f.s.d.Signer.(KeySigner)
	ks.SignErr = errors.New("signer down")
	f.s.d.Signer = ks
	f.clock.add(10)
	require.Error(t, f.s.CloseCircle(ctx, hub.ID))
	row, _ := f.s.load(v.ID)
	assert.Equal(t, db.ListingPhaseListed, row.Phase)
	assert.LessOrEqual(t, row.DueAt, f.clock.Now().Unix(), "due now")
}
