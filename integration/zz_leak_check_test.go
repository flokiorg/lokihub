//go:build integration

// zz_leak_check_test.go's TestZZZ_NoLeakedEphemeralFixtures runs last in
// this package (file name and test name both sort last, which is how `go
// test` orders top-level tests within a package) and asserts every
// ephemeral fixture this run created was torn down by its own t.Cleanup. A
// t.Cleanup that's missing, or that silently swallows a delete failure
// (every one in this suite only logs, per apps.DeleteApp's own child-count
// guard), would otherwise reaccumulate exactly the kind of stale-hub
// backlog that once tipped the relay into rejecting subscriptions with
// "too many concurrent subscription" (see this repo's own history).
package integration

import (
	"testing"
)

func TestZZZ_NoLeakedEphemeralFixtures(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	// jit_wallet children are excluded from this listing entirely (see
	// listJITWalletClaims's own doc comment on api.ListApps) - this only
	// catches leaked hubs/circle_wallet/isolated apps, but a leaked hub is
	// the visible symptom: every child under it was necessarily leaked too.
	apps, err := admin.listAppsByNamePrefix(ephemeralFixtureNamePrefix)
	if err != nil {
		t.Fatalf("failed to list apps by ephemeral fixture name prefix: %v", err)
	}
	if len(apps) == 0 {
		return
	}

	for _, app := range apps {
		t.Errorf("leaked ephemeral fixture: app_id=%d wallet_pubkey=%s - a t.Cleanup somewhere didn't tear this down (delete it via the admin API, then find and fix the missing/failing cleanup)", app.ID, app.WalletPubkey)
	}
}
