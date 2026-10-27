//go:build integration

// ephemeral_test.go provides every throwaway, admin-API-provisioned fixture
// this suite needs to be fully self-sufficient: fresh cash_hub/circle_hub
// apps (either circle policy, including a synthetic Nostr identity that
// publishes a "follow_set"), a simple external-payer wallet, and root self-funding for
// all of them - so no test depends on config.local.yaml naming a real,
// hand-provisioned, long-lived hub. Every helper here skips cleanly if
// admin_api isn't configured.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// ephemeralFixtureNamePrefix tags every app this suite creates directly by
// name, so TestZZZ_NoLeakedEphemeralFixtures can find anything a missing
// t.Cleanup left behind.
const ephemeralFixtureNamePrefix = "integration ephemeral"

// circlePolicyAllowlist/circlePolicyFollowSet mirror db.CirclePolicyAllowlist/
// db.CirclePolicyFollowSet (db/models.go) as local string constants rather
// than importing the db package - this suite stays a black-box NWC/admin-API
// client, not a consumer of internal server packages (see
// integration/README.md).
const (
	circlePolicyAllowlist = "allowlist"
	circlePolicyFollowSet = "follow_set"
)

// kindFollowSet is NIP-51's follow set, which a follow_set circle reads.
const kindFollowSet = 30000

// ephemeralFollowSetD is the d identifier every synthetic provider's set
// uses. Each provider is a fresh keypair, so the address is unique anyway.
const ephemeralFollowSetD = "integration"

// publishFollowSet publishes a real, signed kind-30000 follow set from
// providerPrivkey, with d = ephemeralFollowSetD, naming every one of
// memberPubkeys as a public `p` member, to cfg's own RelayURL
// (config.local.yaml). This is the entire authorization mechanism for a
// follow_set circle the hub only reads (NIP-CW §Membership): publishing it
// makes memberPubkeys (and only them) members of a circle_hub whose identity
// names this provider and d.
//
// cfg.RelayURL must be one of the relays this lokihub instance's own
// GetGeneralRelayUrls() setting (config/config.go) names - that's what the
// backend's follow-set lookup (service/nostr_social_cache.go) queries.
// Deliberately never a public/third-party relay - a real relay this suite
// doesn't control being slow, rate-limiting, or unreachable should never be
// why this suite fails.
//
// MUST be called before the circle_hub itself is created: api.CreateApp
// reads the set at hub-creation time, and a set it can't find yet answers
// NOT_READY until the background refresher reads it again.
func publishFollowSet(t *testing.T, cfg *Config, providerPrivkey string, memberPubkeys []string) {
	t.Helper()
	require.NotEmpty(t, cfg.RelayURL, "config.local.yaml: relay_url must be set - see config.example.yaml")

	tags := nostr.Tags{{"d", ephemeralFollowSetD}}
	for _, pubkey := range memberPubkeys {
		tags = append(tags, nostr.Tag{"p", pubkey})
	}
	ev := nostr.Event{
		Kind:      kindFollowSet,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	require.NoError(t, ev.Sign(providerPrivkey))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relay, err := nostr.RelayConnect(ctx, cfg.RelayURL)
	require.NoError(t, err, "connect to %s to publish provider follow set", cfg.RelayURL)
	defer relay.Close()

	require.NoError(t, relay.Publish(ctx, ev), "publish provider follow set")
}

// ephemeralCircleHubOpts configures an ephemeral circle_hub's own admin-set
// policy knobs. Zero-valued fields get the same defaults circle_hub_test.go's
// happy path has always used, so most callers only need to set what their
// scenario actually varies.
type ephemeralCircleHubOpts struct {
	MinBudgetRenewal  string     // "" -> apps.CreateCircleHub defaults to "monthly" server-side
	MaxExpSecs        int        // must be positive server-side; 0 here defaults to 86400
	PerWalletMaxMloki int        // must be positive server-side; 0 here defaults to 1_000_000
	FeesPpm           int        // 0 is a valid, meaningful value (no fee skim) - never defaulted
	FundLoki          uint64     // 0 here defaults to 100
	ExpiresAt         *time.Time // nil -> the hub's own permission never expires
}

func (o ephemeralCircleHubOpts) withDefaults() ephemeralCircleHubOpts {
	if o.MaxExpSecs == 0 {
		o.MaxExpSecs = 86400
	}
	if o.PerWalletMaxMloki == 0 {
		o.PerWalletMaxMloki = 1_000_000
	}
	if o.FundLoki == 0 {
		o.FundLoki = 100
	}
	return o
}

// createEphemeralCircleHub provisions a throwaway circle_hub of the given
// policy (circlePolicyAllowlist or circlePolicyFollowSet) via the admin API,
// with a brand-new CircleIdentity (never an existing/shared one), authorizes
// each of authorizedPrivkeys under it, and root-funds it (see
// adminClient.transfer) so it can actually mint children. Registers
// t.Cleanup to reclaim every child it ever mints and then delete the hub
// itself.
//
// For circlePolicyFollowSet, the corresponding pubkeys are published as a
// generated provider identity's follow set BEFORE the hub is created (see
// publishFollowSet's own doc comment for why the ordering matters) - the
// provider keypair itself is internal, callers never need it. The hub only
// reads that set; see createEphemeralManagedFollowSetHub for a set the hub
// publishes itself.
// For circlePolicyAllowlist, the hub is created first and members are
// granted afterward via addCircleAllowlistMember, which has no such
// ordering constraint (a synchronous DB check, not a cache).
//
// Returns the hub's ready-to-use CircleHubConfig (Members.AuthorizedPrivkeys
// echoes authorizedPrivkeys back, so callers can use it exactly like a
// config.local.yaml-sourced hub - see mintCircleChild in cross_test.go), the
// admin API's own app id for it, and the adminClient itself.
func createEphemeralCircleHub(t *testing.T, cfg *Config, name, policy string, authorizedPrivkeys []string, opts ephemeralCircleHubOpts) (CircleHubConfig, uint, *adminClient) {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}
	opts = opts.withDefaults()

	authorizedPubkeys := make([]string, len(authorizedPrivkeys))
	for i, priv := range authorizedPrivkeys {
		authorizedPubkeys[i] = mustPubkey(t, priv)
	}

	req := adminCreateAppRequest{
		Name: ephemeralFixtureNamePrefix + " " + name,
		// GET_INFO_SCOPE isn't needed to call create_circle_wallet itself
		// (get_info is always callable regardless of scope - see
		// event_handler.go's GetAlwaysGrantedMethods), but it gates whether
		// get_info's response includes its richer fields at all, including
		// the circle_wallet terms block circle_fee_skim_test.go checks (see
		// get_info_controller.go's own HasPermission(GET_INFO_SCOPE) gate).
		// GET_BALANCE_SCOPE lets delete_test.go's reclaim-on-delete scenarios
		// read the hub's own isolated balance directly.
		Scopes:                  []string{constants.CIRCLE_WALLET_SCOPE, constants.GET_INFO_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                    "circle_hub",
		CircleIdentityName:      ephemeralFixtureNamePrefix + " " + name + " identity",
		CirclePolicy:            policy,
		CircleMaxExpSecs:        opts.MaxExpSecs,
		CircleFeesPpm:           opts.FeesPpm,
		CirclePerWalletMaxMloki: opts.PerWalletMaxMloki,
		CircleMinBudgetRenewal:  opts.MinBudgetRenewal,
	}
	if opts.ExpiresAt != nil {
		req.ExpiresAt = opts.ExpiresAt.Format(time.RFC3339)
	}

	if policy == circlePolicyFollowSet {
		providerPrivkey := newTestPrivkey(t)
		req.ProviderPubkey = mustPubkey(t, providerPrivkey)
		req.FollowSetD = ephemeralFollowSetD
		publishFollowSet(t, cfg, providerPrivkey, authorizedPubkeys)
	}

	resp := createEphemeralCircleHubApp(t, admin, req, opts)

	if policy == circlePolicyAllowlist {
		for _, pubkey := range authorizedPubkeys {
			require.NoError(t, admin.addCircleAllowlistMember(resp.ID, pubkey))
		}
	}

	return CircleHubConfig{
		Name:           name,
		Connection:     resp.PairingUri,
		CircleHubToken: resp.CircleHubToken,
		Members:        CircleMembers{AuthorizedPrivkeys: authorizedPrivkeys},
	}, resp.ID, admin
}

// createEphemeralCircleHubApp creates the circle_hub described by req,
// registers the cleanup that removes its children, the hub and its identity,
// and root-funds it.
func createEphemeralCircleHubApp(t *testing.T, admin *adminClient, req adminCreateAppRequest, opts ephemeralCircleHubOpts) adminCreateAppResponse {
	t.Helper()
	resp, err := admin.createApp(req)
	require.NoError(t, err)

	// Registered first so it runs LAST (t.Cleanup is LIFO), after the hub app
	// below is deleted: apps.DeleteCircleIdentity refuses while any circle_hub
	// still references it, and every ephemeral hub here gets its own
	// brand-new identity (CircleIdentityName is always set, never
	// CircleIdentityId). Without this, the identity itself outlives the hub
	// forever (see TestCreateCircleHub_IdentitySurvivesHubDeletion - by
	// design, identities are meant to be reusable across hubs) and, for
	// circlePolicyFollowSet, keeps getting swept by the background social
	// cache refresher (nostr_social_cache.go's runSocialCacheRefresh) every
	// 5 minutes indefinitely - this was found live accumulating hundreds of
	// leaked relay-backed identities, each still being re-queried
	// against the general relays and contributing to the "too many
	// concurrent REQs" notices/timeouts this suite has hit.
	if identityID, err := admin.getCircleIdentityID(resp.ID); err != nil {
		t.Logf("cleanup: failed to resolve ephemeral circle_hub app_id=%d's own circle identity id, cannot clean it up (%v)", resp.ID, err)
	} else {
		t.Cleanup(func() {
			if err := admin.deleteCircleIdentity(identityID); err != nil {
				t.Logf("cleanup: failed to delete ephemeral circle identity id=%d (%v)", identityID, err)
			}
		})
	}
	// Registered before the sweep below so it runs second (t.Cleanup is
	// LIFO): every child this hub ever mints, across every subtest that
	// uses it, must be reclaimed/deleted before the hub itself can be -
	// apps.DeleteApp refuses a circle_hub with any circle_wallet children
	// still attached.
	t.Cleanup(func() {
		if err := admin.deleteApp(resp.ID); err != nil {
			t.Logf("cleanup: failed to delete ephemeral circle_hub app_id=%d (%v)", resp.ID, err)
		}
	})
	t.Cleanup(func() {
		children, err := admin.listCircleChildren(resp.ID)
		if err != nil {
			t.Logf("cleanup: failed to list circle children for ephemeral hub app_id=%d (%v)", resp.ID, err)
			return
		}
		for _, child := range children {
			if err := admin.deleteCircleChild(resp.ID, child.AppID); err != nil {
				t.Logf("cleanup: failed to delete ephemeral circle child app_id=%d (%v)", child.AppID, err)
			}
		}
	})

	require.NoError(t, admin.transfer(nil, resp.ID, opts.FundLoki))
	return resp
}

// createEphemeralManagedFollowSetHub provisions a throwaway follow_set
// circle_hub whose set the hub publishes itself, as its node identity, with
// set as its first version and d as its identifier. Same cleanup and funding
// as createEphemeralCircleHub.
func createEphemeralManagedFollowSetHub(t *testing.T, cfg *Config, name, d string, set adminFollowSet, opts ephemeralCircleHubOpts) (adminCreateAppResponse, *adminClient) {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}
	opts = opts.withDefaults()
	resp := createEphemeralCircleHubApp(t, admin, adminCreateAppRequest{
		Name:                    ephemeralFixtureNamePrefix + " " + name,
		Scopes:                  []string{constants.CIRCLE_WALLET_SCOPE, constants.GET_INFO_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                    "circle_hub",
		CircleIdentityName:      ephemeralFixtureNamePrefix + " " + name + " identity",
		CirclePolicy:            circlePolicyFollowSet,
		FollowSetD:              d,
		FollowSet:               &set,
		CircleMaxExpSecs:        opts.MaxExpSecs,
		CircleFeesPpm:           opts.FeesPpm,
		CirclePerWalletMaxMloki: opts.PerWalletMaxMloki,
		CircleMinBudgetRenewal:  opts.MinBudgetRenewal,
	}, opts)
	return resp, admin
}

// ephemeralCashHubFundLoki is root-funded into every ephemeral cash_hub
// createEphemeralCashHub mints - comfortably more than the single small test
// child each caller typically mints.
const ephemeralCashHubFundLoki = 2000

// createEphemeralCashHub provisions a throwaway cash_hub app via the admin API
// (POST /api/apps), expiring at expiresAt (nil = never), and root-funds it
// (see adminClient.transfer) so it can actually mint a child. Registers
// t.Cleanup to delete it afterward - this only succeeds if the caller has
// already reclaimed/deleted any child it minted (via the returned admin
// client's deleteCashWallet), matching apps.DeleteApp's own child-count
// guard; a leftover ephemeral hub with a child still attached is logged, not
// failed, so one test's cleanup miss doesn't cascade into unrelated test
// failures.
//
// Returns the hub's ready-to-use CashHubConfig (for mintCashChild et al.), the
// admin API's own app id for it (for deleteCashWallet/deleteApp), and the
// adminClient itself (so callers don't need to re-derive one).
func createEphemeralCashHub(t *testing.T, cfg *Config, name string, expiresAt *time.Time) (CashHubConfig, uint, *adminClient) {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	req := adminCreateAppRequest{
		Name: ephemeralFixtureNamePrefix + " " + name,
		// PAY_INVOICE_SCOPE and MAKE_INVOICE_SCOPE aren't needed for
		// mint_cash itself, but cash_hub_payment_test.go's own
		// mintInvoiceFromHub uses the hub's own make_invoice to fund a real
		// invoice for one of its children to claim against, and
		// TestCrossHub_HubBalance_DecreasesWhenChildMinted probes get_balance -
		// granting all three upfront means those tests exercise the real
		// path instead of skipping for a missing scope.
		Scopes:                []string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.MAKE_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                  "cash_hub",
		CashPerWalletMaxMloki: 10_000_000,
		CashMaxExpSecs:        3600,
	}
	if expiresAt != nil {
		req.ExpiresAt = expiresAt.Format(time.RFC3339)
	}
	resp, err := admin.createApp(req)
	require.NoError(t, err)

	// Registered before the sweep below so it runs second (t.Cleanup is
	// LIFO): every child this hub ever mints, across every subtest that
	// uses it, must be reclaimed/deleted before the hub itself can be -
	// apps.DeleteApp refuses a cash_hub with any cash_wallet children still
	// attached.
	t.Cleanup(func() {
		if err := admin.deleteApp(resp.ID); err != nil {
			t.Logf("cleanup: failed to delete ephemeral cash_hub app_id=%d (%v)", resp.ID, err)
		}
	})
	t.Cleanup(func() {
		claims, err := admin.listCashWalletClaims(resp.ID)
		if err != nil {
			t.Logf("cleanup: failed to list cash wallet children for ephemeral hub app_id=%d (%v)", resp.ID, err)
			return
		}
		seen := map[uint]bool{}
		for _, claim := range claims {
			if seen[claim.WalletAppID] {
				continue
			}
			seen[claim.WalletAppID] = true
			if err := admin.deleteCashWallet(resp.ID, claim.WalletAppID); err != nil {
				t.Logf("cleanup: failed to delete ephemeral cash wallet child app_id=%d (%v)", claim.WalletAppID, err)
			}
		}
	})

	require.NoError(t, admin.transfer(nil, resp.ID, ephemeralCashHubFundLoki))

	return CashHubConfig{Name: name, Connection: resp.PairingUri, CashHubToken: resp.CashHubToken}, resp.ID, admin
}

// ephemeralSimpleWalletFundLoki is root-funded into every ephemeral simple
// wallet createEphemeralSimpleWallet mints - enough headroom to pay several
// small test invoices out to a circle/cash child.
const ephemeralSimpleWalletFundLoki = 2000

// createEphemeralSimpleWallet provisions a throwaway, plain isolated app
// (not a cash_hub/circle_hub) granted make_invoice+pay_invoice, root-funded
// so it can act as an external payer/payee - the self-provisioned
// replacement for config.local.yaml's simple_wallet. Registers t.Cleanup to
// delete it (a bare isolated app with no children of its own, so
// apps.DeleteApp's child-count guard never applies here).
func createEphemeralSimpleWallet(t *testing.T, cfg *Config) SimpleWalletConfig {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	resp, err := admin.createApp(adminCreateAppRequest{
		Name:   ephemeralFixtureNamePrefix + " simple wallet",
		Scopes: []string{constants.MAKE_INVOICE_SCOPE, constants.PAY_INVOICE_SCOPE},
		Kind:   "isolated",
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := admin.deleteApp(resp.ID); err != nil {
			t.Logf("cleanup: failed to delete ephemeral simple wallet app_id=%d (%v)", resp.ID, err)
		}
	})

	require.NoError(t, admin.transfer(nil, resp.ID, ephemeralSimpleWalletFundLoki))

	return SimpleWalletConfig{Connection: resp.PairingUri}
}

// createEphemeralTrustedIA generates a fresh Identity Authority keypair and
// registers its pubkey as trusted via the admin API (POST
// /api/identity-authorities) - the self-provisioned replacement for
// config.local.yaml's cash_hubs[].trusted_ia_privkey, used by
// connection_key-mode scenarios (cash_hub_test.go's
// CreateWallet_ConnectionKeyMode_HappyPath, cash_redeem_test.go's
// connection_key-mode claiming). Registers t.Cleanup to revoke it again.
func createEphemeralTrustedIA(t *testing.T, cfg *Config) string {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	privkey := newTestPrivkey(t)
	pubkey := mustPubkey(t, privkey)
	require.NoError(t, admin.registerIdentityAuthority(pubkey, ephemeralFixtureNamePrefix+" trusted IA"))
	t.Cleanup(func() {
		if err := admin.deleteIdentityAuthority(pubkey); err != nil {
			t.Logf("cleanup: failed to revoke ephemeral trusted IA %s (%v)", pubkey, err)
		}
	})
	return privkey
}

// addEphemeralCircleAllowlistMember grants a freshly-generated, disposable
// keypair membership on an already-created allowlist-policy hub via the
// admin API - for a test that needs to authorize a member *after* the hub
// exists (createEphemeralCircleHub's own authorizedPrivkeys covers the more
// common "known upfront" case). Registers t.Cleanup to revoke it again.
func addEphemeralCircleAllowlistMember(t *testing.T, cfg *Config, hubClient *nwcclient.Client) string {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	hubAppID, err := admin.findAppIDByWalletPubkey(hubClient.WalletPubkey())
	require.NoError(t, err)

	privkey := newTestPrivkey(t)
	pubkey := mustPubkey(t, privkey)
	require.NoError(t, admin.addCircleAllowlistMember(hubAppID, pubkey))
	t.Cleanup(func() {
		if err := admin.removeCircleAllowlistMember(hubAppID, pubkey); err != nil {
			t.Logf("cleanup: failed to remove ephemeral circle allowlist member %s from hub app_id=%d (%v)", pubkey, hubAppID, err)
		}
	})
	return privkey
}
