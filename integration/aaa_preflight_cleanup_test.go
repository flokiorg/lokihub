//go:build integration

// aaa_preflight_cleanup_test.go's TestAAA_PreflightCleanup runs FIRST in this
// package (file name and test name both sort first - the mirror image of
// zz_leak_check_test.go's own "runs last" trick, which relies on the same
// file-name/test-name sort order). It best-effort-sweeps any ephemeral app
// or circle identity left over from a run that never got to finish (Ctrl-C,
// CI timeout, panic before t.Cleanup fired) - see issues.md's investigation,
// which found this DB had accumulated thousands of leaked identities across
// past runs - so every run starts from the same, guaranteed-empty baseline
// instead of inheriting whatever a prior interrupted run happened to leave
// behind.
//
// Deliberately separate from TestZZZ_NoLeakedEphemeralFixtures/
// TestZZZ_NoLeakedEphemeralCircleIdentities, which stay strict (fail loudly,
// never auto-clean): those are THIS run's own regression detector for a
// t.Cleanup that's missing or silently swallowing an error. Auto-cleaning
// there would let a genuinely broken cleanup path hide forever behind "the
// preflight sweep will catch it next time." This file only ever mops up
// debris from a run that already ended (successfully or not) before this one
// started - never anything the current run itself created.
package integration

import "testing"

func TestAAA_PreflightCleanup(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	apps, err := admin.listAppsByNamePrefix(ephemeralFixtureNamePrefix)
	if err != nil {
		t.Logf("preflight: failed to list stale ephemeral apps: %v", err)
	}
	for _, app := range apps {
		// Reclaim children first - apps.DeleteApp refuses a hub with any
		// circle_wallet/cash_wallet child still attached, the same guard
		// every ephemeral fixture's own t.Cleanup already respects (see
		// createEphemeralCircleHub/createEphemeralCashHub).
		if children, err := admin.listCircleChildren(app.ID); err == nil {
			for _, child := range children {
				if err := admin.deleteCircleChild(app.ID, child.AppID); err != nil {
					t.Logf("preflight: failed to delete stale circle child app_id=%d under hub app_id=%d: %v", child.AppID, app.ID, err)
				}
			}
		}
		if claims, err := admin.listCashWalletClaims(app.ID); err == nil {
			seen := map[uint]bool{}
			for _, claim := range claims {
				if seen[claim.WalletAppID] {
					continue
				}
				seen[claim.WalletAppID] = true
				if err := admin.deleteCashWallet(app.ID, claim.WalletAppID); err != nil {
					t.Logf("preflight: failed to delete stale cash wallet app_id=%d under hub app_id=%d: %v", claim.WalletAppID, app.ID, err)
				}
			}
		}
		if err := admin.deleteApp(app.ID); err != nil {
			t.Logf("preflight: failed to delete stale ephemeral app_id=%d: %v", app.ID, err)
		} else {
			t.Logf("preflight: swept stale ephemeral app_id=%d", app.ID)
		}
	}

	// CircleIdentity rows deliberately survive their circle_hub app's own
	// deletion (see TestCreateCircleHub_IdentitySurvivesHubDeletion), so this
	// runs after the apps above are gone - deleteCircleIdentity refuses while
	// any circle_hub app still references it.
	identities, err := admin.listCircleIdentitiesByNamePrefix(ephemeralFixtureNamePrefix)
	if err != nil {
		t.Logf("preflight: failed to list stale ephemeral circle identities: %v", err)
	}
	for _, identity := range identities {
		if err := admin.deleteCircleIdentity(identity.ID); err != nil {
			t.Logf("preflight: failed to delete stale circle identity id=%d: %v", identity.ID, err)
		} else {
			t.Logf("preflight: swept stale circle identity id=%d", identity.ID)
		}
	}
}
