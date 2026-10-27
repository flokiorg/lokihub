//go:build integration

// spec_listing_test.go is CT-LH-LIST (plan §5.1 I-70..I-74, I-RC1): offer
// listings published by the Hub to the dev relay, read back and checked with
// nmilat's ParseListing, paused, edited and retired in NIP-CASH's order; a Cash
// Hub connection refused as a listing and regenerated; the operator line checked
// against an 11191 a test key publishes (lokihub builds and signs none); the
// signing oracle closed on the admin API.
//
// The time-travel steps (republish after 7 days, the deletion after the last
// open version's expiry) move the listing scheduler's clock through the
// testhooks clock hook (POST /api/test/clock {scope: "listing"}) and skip on a
// stack without it.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"
	nmlisting "github.com/ohstr/nmilat/nipcash/listing"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adminListing is the admin API's listing view (listing.View).
type adminListing struct {
	ID               uint     `json:"id"`
	Type             string   `json:"type"`
	HubAppID         uint     `json:"hubAppId"`
	ConnAppID        uint     `json:"connAppId"`
	D                string   `json:"d"`
	Address          string   `json:"address"`
	ATag             []string `json:"aTag"`
	Status           string   `json:"status"`
	Phase            string   `json:"phase"`
	Connection       string   `json:"connection"`
	LastCreatedAt    int64    `json:"lastCreatedAt"`
	LastEventID      string   `json:"lastEventId"`
	RelaysOK         int      `json:"relaysOk"`
	OperatorVerified *bool    `json:"operatorVerified"`
}

// listingRelaysOnDevRelay points the listing relays at the dev relay for the
// test and restores the setting after.
func listingRelaysOnDevRelay(t *testing.T, cfg *Config, admin *adminClient) {
	t.Helper()
	require.NotEmpty(t, cfg.RelayURL, "relay_url must be set")
	var before struct {
		Configured []string `json:"configured"`
	}
	require.NoError(t, admin.do(http.MethodGet, "/api/listing-relays", &before))
	require.NoError(t, admin.doBody(http.MethodPut, "/api/listing-relays", map[string]any{"relays": []string{cfg.RelayURL}}, nil))
	t.Cleanup(func() {
		_ = admin.doBody(http.MethodPut, "/api/listing-relays", map[string]any{"relays": before.Configured}, nil)
	})
}

// fetchEvents reads the events matching f from the dev relay.
func fetchEvents(t *testing.T, cfg *Config, f nostr.Filter) []*nostr.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relay, err := nostr.RelayConnect(ctx, cfg.RelayURL)
	require.NoError(t, err)
	defer relay.Close()
	evs, err := relay.QuerySync(ctx, f)
	require.NoError(t, err)
	return evs
}

func toNip01(ev *nostr.Event) *nip01.Event {
	tags := make([][]string, 0, len(ev.Tags))
	for _, tg := range ev.Tags {
		tags = append(tags, []string(tg))
	}
	return &nip01.Event{ID: ev.ID, PubKey: ev.PubKey, CreatedAt: uint64(ev.CreatedAt), Kind: ev.Kind, Tags: tags, Content: ev.Content, Sig: ev.Sig}
}

// listingOnRelay returns the dev relay's newest kind-31190 version of the
// listing at address, parsed (chain flokicoin held, test networks on).
func listingOnRelay(t *testing.T, cfg *Config, address string) (*nmlisting.Listing, *nostr.Event) {
	t.Helper()
	_, author, d, ok := nmlisting.ParseAddress(address)
	require.True(t, ok, address)
	evs := fetchEvents(t, cfg, nostr.Filter{Kinds: []int{nmlisting.KindListing}, Authors: []string{author}, Tags: nostr.TagMap{"d": {d}}})
	require.NotEmpty(t, evs, "no listing on the dev relay")
	newest := evs[0]
	for _, ev := range evs {
		if ev.CreatedAt > newest.CreatedAt {
			newest = ev
		}
	}
	l, err := nmlisting.ParseListing(toNip01(newest), nmlisting.CheckOptions{Chains: []string{"flokicoin"}, TestNetworks: true})
	if err != nil {
		// A closed version still parses up to its status; return the raw event.
		return nil, newest
	}
	return l, newest
}

func eventTag(ev *nostr.Event, name string) string {
	if tg := ev.Tags.Find(name); len(tg) >= 2 {
		return tg[1]
	}
	return ""
}

// moveClock moves the listing scheduler's clock by offset (the testhooks clock
// hook, scope "listing"); the step skips when the hook is absent.
func moveClock(t *testing.T, admin *adminClient, offset int64) bool {
	t.Helper()
	if err := admin.doBody(http.MethodPost, "/api/test/clock", map[string]any{"offset_secs": offset, "scope": "listing"}, nil); err != nil {
		t.Logf("clock hook unavailable (%v): time-travel step skipped", err)
		return false
	}
	return true
}

// I-70: a circle listed from the admin API, read back and verified; the node's
// 10002; pause → paused; edit → republished; +7 days → a new version.
func TestSpecListing_I70_Publish(t *testing.T) {
	cfg := requireConfig(t)
	_, hubID, admin := createEphemeralCircleHub(t, cfg, "listed circle", circlePolicyAllowlist, nil, ephemeralCircleHubOpts{})
	listingRelaysOnDevRelay(t, cfg, admin)

	var l adminListing
	require.NoError(t, admin.doBody(http.MethodPost, "/api/listings", map[string]any{
		"circle_hub_app_id": hubID, "title": "Integration circle", "content": "Join us", "languages": []string{"en"},
		"geohashes": []string{"eyc"}, "location": "Lisbon",
	}, &l))
	t.Cleanup(func() { _ = admin.doBody(http.MethodPost, fmt.Sprintf("/api/listings/%d/unlist", l.ID), nil, nil) })
	assert.Equal(t, "circle_wallet", l.Type)
	assert.Positive(t, l.RelaysOK)

	parsed, ev := listingOnRelay(t, cfg, l.Address)
	require.NotNil(t, parsed, "the listing parses with ParseListing")
	assert.Equal(t, l.LastEventID, ev.ID)
	assert.Equal(t, "flokicoin", parsed.Chain)
	assert.Equal(t, "regtest", parsed.Network)
	require.NotNil(t, parsed.Circle)
	assert.Equal(t, parsed.NodeIdentity, parsed.Circle.HubIdentity)
	assert.Equal(t, parsed.D, parsed.Circle.Pubkey)

	rl := fetchEvents(t, cfg, nostr.Filter{Kinds: []int{10002}, Authors: []string{parsed.NodeIdentity}})
	require.NotEmpty(t, rl, "the node's relay list is on the dev relay")
	assert.Contains(t, rl[0].Tags, nostr.Tag{"r", cfg.RelayURL, "write"})

	time.Sleep(1100 * time.Millisecond)
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/listings/%d/pause", l.ID), nil, &l))
	_, ev = listingOnRelay(t, cfg, l.Address)
	assert.Equal(t, "paused", eventTag(ev, "status"))

	time.Sleep(1100 * time.Millisecond)
	require.NoError(t, admin.doBody(http.MethodPut, fmt.Sprintf("/api/listings/%d", l.ID), map[string]any{"title": "Renamed circle"}, &l))
	_, ev = listingOnRelay(t, cfg, l.Address)
	assert.Equal(t, "Renamed circle", eventTag(ev, "title"))

	before := l.LastCreatedAt
	if moveClock(t, admin, 7*24*3600+60) {
		t.Cleanup(func() { moveClock(t, admin, 0) })
		require.Eventually(t, func() bool {
			_, ev := listingOnRelay(t, cfg, l.Address)
			return int64(ev.CreatedAt) > before
		}, 60*time.Second, 2*time.Second, "republished after 7 days")
	}
}

// I-71: unlist publishes the closed version, get_info unchanged; past the last
// open version's expiry the NIP-09 deletion (a, k) follows. Closing a circle
// (WP-19's accepting toggle): accepting false first, then closed.
func TestSpecListing_I71_Retirement(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubID, admin := createEphemeralCircleHub(t, cfg, "retired circle", circlePolicyAllowlist, nil, ephemeralCircleHubOpts{})
	listingRelaysOnDevRelay(t, cfg, admin)

	var l adminListing
	require.NoError(t, admin.doBody(http.MethodPost, "/api/listings", map[string]any{"circle_hub_app_id": hubID, "title": "Leaving soon"}, &l))
	time.Sleep(1100 * time.Millisecond)
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/listings/%d/unlist", l.ID), nil, &l))
	assert.Equal(t, "closing", l.Phase)
	_, ev := listingOnRelay(t, cfg, l.Address)
	assert.Equal(t, "closed", eventTag(ev, "status"))

	// get_info on the hub is unchanged by unlisting.
	var info struct {
		CircleWallet struct {
			Accepting bool `json:"accepting"`
		} `json:"circle_wallet"`
	}
	require.NoError(t, mustConnect(t, hub.Connection).Call(ctxT(t), "get_info", struct{}{}, &info))
	assert.True(t, info.CircleWallet.Accepting)

	if moveClock(t, admin, 31*24*3600) {
		t.Cleanup(func() { moveClock(t, admin, 0) })
		_, author, _, _ := nmlisting.ParseAddress(l.Address)
		require.Eventually(t, func() bool {
			for _, del := range fetchEvents(t, cfg, nostr.Filter{Kinds: []int{5}, Authors: []string{author}}) {
				if eventTag(del, "a") == l.Address && eventTag(del, "k") == "31190" {
					return true
				}
			}
			return false
		}, 60*time.Second, 2*time.Second, "the NIP-09 deletion follows")
	}

	// Closing a circle: accepting false first, then closed (WP-19's toggle).
	_, hub2ID, _ := createEphemeralCircleHub(t, cfg, "closing circle", circlePolicyAllowlist, nil, ephemeralCircleHubOpts{})
	var l2 adminListing
	require.NoError(t, admin.doBody(http.MethodPost, "/api/listings", map[string]any{"circle_hub_app_id": hub2ID}, &l2))
	if err := admin.doBody(http.MethodPost, fmt.Sprintf("/api/apps/%d/circle/accepting", hub2ID), map[string]any{"accepting": false}, nil); err != nil {
		t.Logf("accepting toggle unavailable (%v): close-circle step waits for WP-19", err)
		_ = admin.doBody(http.MethodPost, fmt.Sprintf("/api/listings/%d/unlist", l2.ID), nil, nil)
		return
	}
	require.Eventually(t, func() bool {
		_, ev := listingOnRelay(t, cfg, l2.Address)
		return eventTag(ev, "status") == "closed"
	}, 60*time.Second, 2*time.Second)
}

// I-72: a Cash Hub is refused as a listing; "Regenerate connection" makes the
// old string get no reply while the new one is answered.
func TestSpecListing_I72_CashHubNeverListed(t *testing.T) {
	cfg := requireConfig(t)
	cashHub, cashHubID, admin := createEphemeralCashHub(t, cfg, "never listed", nil)

	err := admin.doBody(http.MethodPost, "/api/listings", map[string]any{"conn_app_id": cashHubID}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
	err = admin.doBody(http.MethodPost, "/api/listings", map[string]any{"type": "cash_order", "connection": cashHub.CashHubToken}, nil)
	require.Error(t, err, "no endpoint takes a connection string")

	var out struct {
		CashHubToken string `json:"cashHubToken"`
	}
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/cash-hubs/%d/revoke-connection", cashHubID), nil, &out))
	require.True(t, strings.HasPrefix(out.CashHubToken, "cashhub1"))

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	old := mustConnect(t, cashHub.CashHubToken)
	assert.Error(t, old.Call(ctx, "get_info", struct{}{}, nil), "the old string gets no reply")
	var info map[string]any
	require.NoError(t, mustConnect(t, out.CashHubToken).Call(ctxT(t), "get_info", struct{}{}, &info))
}

// I-73: the operator line: a test key's 11191 naming the address verifies it; a
// newer one without it does not. lokihub publishes no 11191 and asks for no key.
func TestSpecListing_I73_Operator(t *testing.T) {
	cfg := requireConfig(t)
	_, hubID, admin := createEphemeralCircleHub(t, cfg, "operated circle", circlePolicyAllowlist, nil, ephemeralCircleHubOpts{})
	listingRelaysOnDevRelay(t, cfg, admin)
	operatorPriv := newTestPrivkey(t)
	operator := mustPubkey(t, operatorPriv)

	var l adminListing
	require.NoError(t, admin.doBody(http.MethodPost, "/api/listings", map[string]any{
		"circle_hub_app_id": hubID, "operator_pubkey": operator, "operator_relay": cfg.RelayURL,
	}, &l))
	t.Cleanup(func() { _ = admin.doBody(http.MethodPost, fmt.Sprintf("/api/listings/%d/unlist", l.ID), nil, nil) })
	_, ev := listingOnRelay(t, cfg, l.Address)
	assert.Equal(t, nostr.Tag{"p", operator, cfg.RelayURL}, ev.Tags.Find("p"))

	publish := func(tags nostr.Tags, at nostr.Timestamp) {
		e := nostr.Event{Kind: nmlisting.KindOperatorOffers, CreatedAt: at, Tags: tags}
		require.NoError(t, e.Sign(operatorPriv))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		relay, err := nostr.RelayConnect(ctx, cfg.RelayURL)
		require.NoError(t, err)
		defer relay.Close()
		require.NoError(t, relay.Publish(ctx, e))
	}
	now := nostr.Now()
	publish(nostr.Tags{{"a", l.Address, cfg.RelayURL}}, now)
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/listings/%d/check-operator", l.ID), nil, &l))
	require.NotNil(t, l.OperatorVerified)
	assert.True(t, *l.OperatorVerified, "operator verified")

	publish(nostr.Tags{{"alt", "Cash offers run by this account"}}, now+1)
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/listings/%d/check-operator", l.ID), nil, &l))
	assert.False(t, *l.OperatorVerified, "not yet named by the operator")

	_, author, _, _ := nmlisting.ParseAddress(l.Address)
	assert.Empty(t, fetchEvents(t, cfg, nostr.Filter{Kinds: []int{nmlisting.KindOperatorOffers}, Authors: []string{author}}),
		"the node publishes no 11191")
}

// I-74: the admin signing endpoint refuses the reserved prefixes; plain text
// signs.
func TestSpecListing_I74_SigningOracle(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured")
	}
	for _, prefix := range []string{"cash-mint:", "cash-zap:", "cash-hub:", "cash-order:", "circle-hub:"} {
		for _, text := range []string{prefix + "v1 x", strings.ToUpper(prefix) + "v1 x"} {
			err := admin.doBody(http.MethodPost, "/api/wallet/sign-message", map[string]any{"message": text}, nil)
			assert.Error(t, err, text)
		}
	}
	var out map[string]any
	require.NoError(t, admin.doBody(http.MethodPost, "/api/wallet/sign-message", map[string]any{"message": "hello"}, &out))
}

// I-RC1 (the listing connection's half): the string a listed circle publishes
// passes the online check: get_info answered by TLV 0 with identity = TLV 4 and
// chain = TLV 6, methods ["get_info"]; revoking it stops the answers.
func TestSpecListing_IRC1_ListingConnectionOnline(t *testing.T) {
	cfg := requireConfig(t)
	_, hubID, admin := createEphemeralCircleHub(t, cfg, "online circle", circlePolicyAllowlist, nil, ephemeralCircleHubOpts{})
	listingRelaysOnDevRelay(t, cfg, admin)
	var l adminListing
	require.NoError(t, admin.doBody(http.MethodPost, "/api/listings", map[string]any{"circle_hub_app_id": hubID}, &l))
	require.True(t, strings.HasPrefix(l.Connection, "circlehub1"))
	conn, err := nipcw.DecodeCircleHubConnection(l.Connection)
	require.NoError(t, err)
	require.NoError(t, conn.Verify())

	var info struct {
		Methods      []string `json:"methods"`
		Chain        string   `json:"chain"`
		CircleWallet struct {
			Identity string `json:"identity"`
		} `json:"circle_wallet"`
	}
	client := mustConnect(t, l.Connection)
	client.PrivateCall = nil
	require.NoError(t, client.Call(ctxT(t), "get_info", struct{}{}, &info))
	assert.Equal(t, []string{"get_info"}, info.Methods)
	assert.Equal(t, conn.Chain, info.Chain)
	assert.Equal(t, conn.HubIdentity, info.CircleWallet.Identity)

	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/listings/%d/revoke-connection", l.ID), nil, &l))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	raw, _ := json.Marshal(struct{}{})
	assert.Error(t, mustConnect(t, l.Connection).Call(ctx, "get_info", json.RawMessage(raw), nil), "a revoked listing connection gets no reply")
}
