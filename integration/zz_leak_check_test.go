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
	"strings"
	"testing"
	"time"
)

func TestZZZ_NoLeakedEphemeralFixtures(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	// cash_wallet children are excluded from this listing entirely (see
	// listCashWalletClaims's own doc comment on api.ListApps) - this only
	// catches leaked hubs/circle_wallet/isolated apps, but a leaked hub is
	// the visible symptom: every child under it was necessarily leaked too.
	//
	// Two kinds of leftover are expected and waited for or accepted:
	//   - a torn-down circle_hub and its Closing wallets, which the lifecycle
	//     job deletes once their (short, ephemeral) grace ends;
	//   - a cash_hub kept only by its retained request records (NIP-CASH Q23),
	//     its orders (NIP-CO §Reading an Order) or its on-chain payouts and
	//     their records (NIP-CASH §Lifecycle and Deletion, Hub teardown).
	var leaked []adminApp
	deadline := time.Now().Add(ephemeralCircleGraceSecs*time.Second + 3*time.Minute)
	for {
		apps, err := admin.listAppsByNamePrefix(ephemeralFixtureNamePrefix)
		if err != nil {
			t.Fatalf("failed to list apps by ephemeral fixture name prefix: %v", err)
		}
		leaked = leaked[:0]
		circles := 0
		for _, app := range apps {
			switch app.Kind {
			case "circle_hub", "circle_wallet":
				circles++
				continue
			case "cash_hub":
				blockers, err := admin.cashHubDeleteBlockers(app.ID)
				if err == nil && onlyRecords(blockers) {
					continue
				}
				// Kept only while its records were retained, its test's cleanup
				// could not delete it then; nothing keeps it now.
				if err == nil && len(blockers) == 0 && admin.deleteApp(app.ID) == nil {
					continue
				}
			}
			leaked = append(leaked, app)
		}
		if circles == 0 || time.Now().After(deadline) {
			if circles > 0 {
				t.Errorf("%d torn-down circle hub(s) or wallet(s) are still there after their grace", circles)
			}
			break
		}
		time.Sleep(5 * time.Second)
	}
	for _, app := range leaked {
		t.Errorf("leaked ephemeral fixture: app_id=%d kind=%s wallet_pubkey=%s - a t.Cleanup somewhere didn't tear this down (delete it via the admin API, then find and fix the missing/failing cleanup)", app.ID, app.Kind, app.WalletPubkey)
	}

	// The identities of torn-down hubs can go now their hubs are gone.
	pendingCircleIdentities.Range(func(key, _ any) bool {
		if err := admin.deleteCircleIdentity(key.(uint)); err != nil && !strings.Contains(err.Error(), "not found") {
			t.Logf("cleanup: failed to delete ephemeral circle identity id=%d (%v)", key, err)
		}
		return true
	})
}

func onlyRecords(blockers []string) bool {
	if len(blockers) == 0 {
		return false
	}
	for _, b := range blockers {
		if b != "records" && b != "orders" && b != "payout_open" && b != "withdraw_records" {
			return false
		}
	}
	return true
}

// TestZZZ_NoLeakedEphemeralCircleIdentities is zz_leak_check_test.go's
// second check, for a table TestZZZ_NoLeakedEphemeralFixtures can't see at
// all: CircleIdentity rows deliberately survive their circle_hub app's own
// deletion (see TestCreateCircleHub_IdentitySurvivesHubDeletion), so
// createEphemeralCircleHub's own t.Cleanup must explicitly delete the
// identity it created too, not just the hub app. A missing/failing identity
// cleanup here doesn't just bloat the DB - a leaked "follow_set"-policy
// identity keeps getting re-fetched from the general relays by
// nostr_social_cache.go's background refresher every
// socialCacheRefreshInterval, indefinitely, and was found live responsible
// for hundreds of accumulated identities contributing to "too many
// concurrent REQs" relay notices/timeouts elsewhere in this suite.
func TestZZZ_NoLeakedEphemeralCircleIdentities(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	identities, err := admin.listCircleIdentitiesByNamePrefix(ephemeralFixtureNamePrefix)
	if err != nil {
		t.Fatalf("failed to list circle identities by ephemeral fixture name prefix: %v", err)
	}
	for _, identity := range identities {
		t.Errorf("leaked ephemeral circle identity: id=%d name=%q - a t.Cleanup somewhere didn't tear this down (delete it via the admin API, then find and fix the missing/failing cleanup)", identity.ID, identity.Name)
	}
}
