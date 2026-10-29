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
// R2-L5: an operator's write relays are taken only when public wss relays.
func TestPublicRelay(t *testing.T) {
	for url, ok := range map[string]bool{
		"wss://relay.example.com":      true,
		"wss://relay.example.com:4443": true,
		"wss://8.8.8.8":                true,
		"ws://relay.example.com":       false,
		"https://relay.example.com":    false,
		"wss://localhost":              false,
		"wss://127.0.0.1":              false,
		"wss://10.0.0.5":               false,
		"wss://192.168.1.2":            false,
		"wss://169.254.169.254":        false,
		"wss://[::1]":                  false,
		"wss://relay.local":            false,
		"wss://intranet":               false,
		"wss://user@relay.example.com": false,
	} {
		assert.Equal(t, ok, publicRelay(url), url)
	}
}
