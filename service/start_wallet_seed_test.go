package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// TestForEachAppWithWallet_PagesEveryWalletAppOnce pins the keyset paging that
// bounds the startup wallet seeders (WS6-B2): every app with a wallet_pubkey is
// visited exactly once in ascending id order, apps without one are skipped, and
// the work is split into bounded pages rather than one unbounded Find.
func TestForEachAppWithWallet_PagesEveryWalletAppOnce(t *testing.T) {
	ts, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer ts.Remove()
	svc := &service{db: ts.DB}

	const withWallet = 25
	want := make(map[uint]bool, withWallet)
	for i := 0; i < withWallet; i++ {
		pk := tests.RandomHex32()
		app := db.App{Name: "w", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHub, WalletPubkey: &pk}
		require.NoError(t, ts.DB.Create(&app).Error)
		want[app.ID] = true
	}
	// Apps without a wallet_pubkey must be excluded.
	for i := 0; i < 5; i++ {
		app := db.App{Name: "n", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHub}
		require.NoError(t, ts.DB.Create(&app).Error)
	}

	var seen []uint
	var pageSizes []int
	require.NoError(t, svc.forEachAppWithWallet(10, func(apps []db.App) {
		pageSizes = append(pageSizes, len(apps))
		for _, a := range apps {
			require.NotNil(t, a.WalletPubkey, "only apps with a wallet_pubkey are yielded")
			seen = append(seen, a.ID)
		}
	}))

	assert.Len(t, seen, withWallet, "every wallet app is visited")
	got := make(map[uint]bool, len(seen))
	for i, id := range seen {
		assert.False(t, got[id], "no app visited twice")
		got[id] = true
		assert.True(t, want[id], "only the wallet apps are visited")
		if i > 0 {
			assert.Less(t, seen[i-1], seen[i], "visited in ascending id order")
		}
	}
	assert.Equal(t, []int{10, 10, 5}, pageSizes, "25 rows at pageSize 10 → pages of 10, 10, 5")
}
