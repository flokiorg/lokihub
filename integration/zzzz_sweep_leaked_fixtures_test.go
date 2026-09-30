//go:build integration

// zzzz_sweep_leaked_fixtures_test.go is the repair tool for what
// zz_leak_check_test.go reports. It is OPT-IN and skips by default, because it
// deletes things: set LOKIHUB_SWEEP_LEAKED_FIXTURES=1 to run it.
//
// It exists because the leak it cleans up is not hypothetical and not one-off.
// A leaked "following"-policy circle identity keeps getting re-fetched from the
// general relays by nostr_social_cache.go's background refresher, forever, and
// has already been found live responsible for accumulated identities
// contributing to "too many concurrent REQs" notices and timeouts elsewhere in
// this suite. So the backlog is not inert: it actively degrades every later run
// against the same backend, which makes "clean it up by hand via the admin API"
// the wrong long-term answer.
//
// Named to sort after the leak checks it repairs, so a normal run reports the
// leak first and this skips last.
//
// Deliberately NOT a replacement for TestAAA_PreflightCleanup, which already
// attempts the same deletions at the start of every run. The two differ in the
// ways that matter here:
//
//   - preflight is best-effort and single-pass, and LOGS every failure. That is
//     the right behaviour for something that must never block a run — but it is
//     also exactly why this leak went unnoticed for so long: preflight had been
//     failing to reclaim these fixtures on every run, into a log nobody reads,
//     while the loud signal (TestZZZ_NoLeakedEphemeralFixtures) was the only
//     thing saying so.
//   - this one runs to a fixed point, CLASSIFIES the refusals, and reads the
//     outcome back from the server rather than reporting what it believes it
//     did. The refusal classification is the diagnostic: grouping 108 refusals
//     into three symmetrical sets of 27 is what identified the root cause as a
//     settling-payment guard rather than the circle identity it appeared to be.
//
// So: preflight keeps runs clean quietly; this exists to find out WHY when it
// cannot.
package integration

import (
	"os"
	"testing"
)

const sweepLeakedFixturesEnv = "LOKIHUB_SWEEP_LEAKED_FIXTURES"

// sweepMaxPasses bounds the fixed-point loop below. Deletion order matters —
// a hub refuses to go while it has children — and the admin API exposes no
// single cascading delete, so the sweep runs repeatedly and lets each pass
// unblock the next. Three levels of nesting (identity <- hub <- child) plus one
// pass to observe that nothing moved is the deepest chain here.
const sweepMaxPasses = 5

func TestZZZZ_SweepLeakedEphemeralFixtures(t *testing.T) {
	if os.Getenv(sweepLeakedFixturesEnv) != "1" {
		t.Skipf("skipping: destructive; set %s=1 to sweep what TestZZZ_NoLeakedEphemeralFixtures reported", sweepLeakedFixturesEnv)
	}

	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	// Every refusal is recorded rather than failed on. A refusal is the
	// INTERESTING output here, not an error: it names what still pins a fixture,
	// which is the open question about why these accumulate at all.
	type refusal struct {
		what string
		err  error
	}
	var refusals []refusal

	remaining := -1
	for pass := 1; pass <= sweepMaxPasses; pass++ {
		apps, err := admin.listAppsByNamePrefix(ephemeralFixtureNamePrefix)
		if err != nil {
			t.Fatalf("pass %d: listing ephemeral fixtures: %v", pass, err)
		}
		if len(apps) == 0 {
			t.Logf("pass %d: no leaked apps left", pass)
			break
		}
		// No progress since the previous pass means the rest is genuinely stuck,
		// not merely waiting on an ordering that another pass would resolve.
		if len(apps) == remaining {
			t.Logf("pass %d: %d app(s) left and nothing moved since the last pass; stopping", pass, len(apps))
			break
		}
		remaining = len(apps)
		t.Logf("pass %d: %d leaked app(s) to sweep", pass, len(apps))

		for _, app := range apps {
			// Read the circle identity id BEFORE deleting the app: it is reachable
			// only through the app's own detail route, and a CircleIdentity row
			// deliberately survives its hub's deletion (see
			// TestCreateCircleHub_IdentitySurvivesHubDeletion), so afterwards there
			// is no longer any way to find which identity belonged to this hub.
			identityID, identityErr := admin.getCircleIdentityID(app.ID)

			// circle_wallet children. Not a circle_hub -> this errors, which is
			// simply the answer "no children of that kind" and is not recorded.
			if children, err := admin.listCircleChildren(app.ID); err == nil {
				for _, child := range children {
					if err := admin.deleteCircleChild(app.ID, child.AppID); err != nil {
						refusals = append(refusals, refusal{what: "circle child", err: err})
					}
				}
			}

			// cash_wallet children. Archived rows are skipped: their wallet is
			// already gone, so deleting it again only produces noise.
			if claims, err := admin.listCashWalletClaims(app.ID); err == nil {
				swept := map[uint]bool{}
				for _, claim := range claims {
					if claim.Archived || swept[claim.WalletAppID] {
						continue
					}
					swept[claim.WalletAppID] = true
					if err := admin.deleteCashWallet(app.ID, claim.WalletAppID); err != nil {
						refusals = append(refusals, refusal{what: "cash wallet", err: err})
					}
				}
			}

			if err := admin.deleteApp(app.ID); err != nil {
				refusals = append(refusals, refusal{what: "app", err: err})
				// The identity stays pinned while its hub is alive, so do not try.
				continue
			}

			if identityErr == nil && identityID != 0 {
				if err := admin.deleteCircleIdentity(identityID); err != nil {
					refusals = append(refusals, refusal{what: "circle identity", err: err})
				}
			}
		}
	}

	// Identities last, and separately: they outlive their hub by design, so one
	// can be orphaned with no app left to reach it through.
	identities, err := admin.listCircleIdentitiesByNamePrefix(ephemeralFixtureNamePrefix)
	if err != nil {
		t.Fatalf("listing leaked circle identities: %v", err)
	}
	for _, identity := range identities {
		if err := admin.deleteCircleIdentity(identity.ID); err != nil {
			refusals = append(refusals, refusal{what: "orphaned circle identity", err: err})
		}
	}

	for _, r := range refusals {
		t.Logf("still pinned: %s: %v", r.what, r.err)
	}

	// The outcome, read back from the server rather than inferred from what the
	// sweep believes it did.
	appsLeft, err := admin.listAppsByNamePrefix(ephemeralFixtureNamePrefix)
	if err != nil {
		t.Fatalf("re-listing ephemeral fixtures: %v", err)
	}
	identitiesLeft, err := admin.listCircleIdentitiesByNamePrefix(ephemeralFixtureNamePrefix)
	if err != nil {
		t.Fatalf("re-listing leaked circle identities: %v", err)
	}
	t.Logf("SWEEP RESULT: %d app(s) and %d circle identity(ies) still leaked, %d refusal(s)",
		len(appsLeft), len(identitiesLeft), len(refusals))
	for _, app := range appsLeft {
		t.Logf("  unswept app_id=%d wallet_pubkey=%s", app.ID, app.WalletPubkey)
	}
	for _, identity := range identitiesLeft {
		t.Logf("  unswept identity id=%d name=%q", identity.ID, identity.Name)
	}
}
