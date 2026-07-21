//go:build integration

// ephemeral_test.go provides every throwaway, admin-API-provisioned fixture
// this suite needs to be fully self-sufficient: fresh jit_hub/circle_hub
// apps (either circle policy, including a synthetic Nostr identity for
// "following"), a simple external-payer wallet, and root self-funding for
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

// circlePolicyAllowlist/circlePolicyFollowing mirror db.CirclePolicyAllowlist/
// db.CirclePolicyFollowing (db/models.go) as local string constants rather
// than importing the db package - this suite stays a black-box NWC/admin-API
// client, not a consumer of internal server packages (see
// integration/README.md).
const (
	circlePolicyAllowlist = "allowlist"
	circlePolicyFollowing = "following"
)

// generalRelayURL is this lokihub instance's configured Nostr relay - the
// same one baked into every admin-created app's own pairing URI (see
// adminCreateAppResponse.PairingUri), and confirmed present in this
// instance's GetGeneralRelayUrls() setting (config/config.go), which is what
// the backend's "following" circle-policy check (service/
// nostr_social_cache.go) actually queries kind:3 from. Hardcoded because
// there's no admin endpoint to discover it - update this if the instance's
// own Relay/GeneralRelay settings ever change.
const generalRelayURL = "wss://relay.ohstr.com"

// publishFollowList publishes a real, signed kind:3 (NIP-02 contact list)
// event from providerPrivkey naming every one of followedPubkeys, to
// generalRelayURL - this is the entire authorization mechanism for
// "following"-policy circle hubs: nostrSocialCache.IsAuthorized treats
// "following" as "the provider follows the requester" (provider's own
// kind:3 contains the requester), so publishing this makes followedPubkeys
// (and only them) authorized under a circle_hub using providerPrivkey's
// pubkey as its ProviderPubkey.
//
// MUST be called before the circle_hub itself is created. api.CreateApp's
// AppKindCircleHub branch calls WarmCircleFollowingCache immediately at
// hub-creation time; if this event isn't live on the relay yet, that warm-up
// caches an empty contact list, and the synchronous request path
// (nostrSocialCache.getContactList's peek-first fast path) won't refetch
// until the background refresher eventually runs - so a hub created before
// this publish would see its members as unauthorized for an indeterminate
// time, not immediately after this call returns.
func publishFollowList(t *testing.T, providerPrivkey string, followedPubkeys []string) {
	t.Helper()

	tags := make(nostr.Tags, 0, len(followedPubkeys))
	for _, pubkey := range followedPubkeys {
		tags = append(tags, nostr.Tag{"p", pubkey})
	}
	ev := nostr.Event{
		Kind:      nostr.KindFollowList, // kind 3 - NIP-02 contact list
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	require.NoError(t, ev.Sign(providerPrivkey))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relay, err := nostr.RelayConnect(ctx, generalRelayURL)
	require.NoError(t, err, "connect to %s to publish provider follow list", generalRelayURL)
	defer relay.Close()

	require.NoError(t, relay.Publish(ctx, ev), "publish provider kind:3 follow list")
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
// policy (circlePolicyAllowlist or circlePolicyFollowing) via the admin API,
// with a brand-new CircleIdentity (never an existing/shared one), authorizes
// each of authorizedPrivkeys under it, and root-funds it (see
// adminClient.transfer) so it can actually mint children. Registers
// t.Cleanup to reclaim every child it ever mints and then delete the hub
// itself.
//
// For circlePolicyFollowing, the corresponding pubkeys are published as the
// generated provider identity's kind:3 follow list BEFORE the hub is
// created (see publishFollowList's own doc comment for why the ordering
// matters) - the provider keypair itself is internal, callers never need it.
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

	if policy == circlePolicyFollowing {
		providerPrivkey := newTestPrivkey(t)
		req.ProviderPubkey = mustPubkey(t, providerPrivkey)
		publishFollowList(t, providerPrivkey, authorizedPubkeys)
	}

	resp, err := admin.createApp(req)
	require.NoError(t, err)
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

	if policy == circlePolicyAllowlist {
		for _, pubkey := range authorizedPubkeys {
			require.NoError(t, admin.addCircleAllowlistMember(resp.ID, pubkey))
		}
	}

	return CircleHubConfig{
		Name:       name,
		Connection: resp.PairingUri,
		Members:    CircleMembers{AuthorizedPrivkeys: authorizedPrivkeys},
	}, resp.ID, admin
}

// ephemeralJITHubFundLoki is root-funded into every ephemeral jit_hub
// createEphemeralJITHub mints - comfortably more than the single small test
// child each caller typically mints.
const ephemeralJITHubFundLoki = 2000

// createEphemeralJITHub provisions a throwaway jit_hub app via the admin API
// (POST /api/apps), expiring at expiresAt (nil = never), and root-funds it
// (see adminClient.transfer) so it can actually mint a child. Registers
// t.Cleanup to delete it afterward - this only succeeds if the caller has
// already reclaimed/deleted any child it minted (via the returned admin
// client's deleteJITWallet), matching apps.DeleteApp's own child-count
// guard; a leftover ephemeral hub with a child still attached is logged, not
// failed, so one test's cleanup miss doesn't cascade into unrelated test
// failures.
//
// Returns the hub's ready-to-use JITHubConfig (for mintJITChild et al.), the
// admin API's own app id for it (for deleteJITWallet/deleteApp), and the
// adminClient itself (so callers don't need to re-derive one).
func createEphemeralJITHub(t *testing.T, cfg *Config, name string, expiresAt *time.Time) (JITHubConfig, uint, *adminClient) {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	req := adminCreateAppRequest{
		Name: ephemeralFixtureNamePrefix + " " + name,
		// PAY_INVOICE_SCOPE and MAKE_INVOICE_SCOPE aren't needed for
		// create_jit_wallet itself, but jit_hub_payment_test.go's own
		// mintInvoiceFromHub uses the hub's own make_invoice to fund a real
		// invoice for one of its children to claim against, and
		// TestCrossHub_HubBalance_DecreasesWhenChildMinted probes get_balance -
		// granting all three upfront means those tests exercise the real
		// path instead of skipping for a missing scope.
		Scopes:               []string{constants.JIT_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.MAKE_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                 "jit_hub",
		JITPerWalletMaxMloki: 10_000_000,
		JITMaxExpSecs:        3600,
	}
	if expiresAt != nil {
		req.ExpiresAt = expiresAt.Format(time.RFC3339)
	}
	resp, err := admin.createApp(req)
	require.NoError(t, err)

	// Registered before the sweep below so it runs second (t.Cleanup is
	// LIFO): every child this hub ever mints, across every subtest that
	// uses it, must be reclaimed/deleted before the hub itself can be -
	// apps.DeleteApp refuses a jit_hub with any jit_wallet children still
	// attached.
	t.Cleanup(func() {
		if err := admin.deleteApp(resp.ID); err != nil {
			t.Logf("cleanup: failed to delete ephemeral jit_hub app_id=%d (%v)", resp.ID, err)
		}
	})
	t.Cleanup(func() {
		claims, err := admin.listJITWalletClaims(resp.ID)
		if err != nil {
			t.Logf("cleanup: failed to list jit wallet children for ephemeral hub app_id=%d (%v)", resp.ID, err)
			return
		}
		seen := map[uint]bool{}
		for _, claim := range claims {
			if seen[claim.WalletAppID] {
				continue
			}
			seen[claim.WalletAppID] = true
			if err := admin.deleteJITWallet(resp.ID, claim.WalletAppID); err != nil {
				t.Logf("cleanup: failed to delete ephemeral jit wallet child app_id=%d (%v)", claim.WalletAppID, err)
			}
		}
	})

	require.NoError(t, admin.transfer(nil, resp.ID, ephemeralJITHubFundLoki))

	return JITHubConfig{Name: name, Connection: resp.PairingUri}, resp.ID, admin
}

// ephemeralSimpleWalletFundLoki is root-funded into every ephemeral simple
// wallet createEphemeralSimpleWallet mints - enough headroom to pay several
// small test invoices out to a circle/jit child.
const ephemeralSimpleWalletFundLoki = 2000

// createEphemeralSimpleWallet provisions a throwaway, plain isolated app
// (not a jit_hub/circle_hub) granted make_invoice+pay_invoice, root-funded
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
// config.local.yaml's jit_hubs[].trusted_ia_privkey, used by
// connection_key-mode scenarios (jit_hub_test.go's
// CreateWallet_ConnectionKeyMode_HappyPath, claim_funds_test.go's
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
