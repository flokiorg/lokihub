//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	coclient "github.com/ohstr/nmilat/nipCO/client"
	nmlisting "github.com/ohstr/nmilat/nipcash/listing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listedOrderShop is a fresh Cash Hub selling on one Order connection, listed
// with hints, its listing relays on the dev relay.
func listedOrderShop(t *testing.T, cfg *Config, name string) (hubID, connID uint, l adminListing, admin *adminClient, connStr string) {
	t.Helper()
	_, hubID, admin = createEphemeralCashHub(t, cfg, name, nil)
	listingRelaysOnDevRelay(t, cfg, admin)
	require.NoError(t, admin.doBody(http.MethodPut, fmt.Sprintf("/api/cash-hubs/%d/order-terms", hubID), orderTerms(), nil))
	var conn struct {
		ID               uint   `json:"id"`
		ConnectionString string `json:"connectionString"`
	}
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/cash-hubs/%d/order-connections", hubID),
		map[string]string{"label": "listed shop"}, &conn))
	require.NoError(t, admin.doBody(http.MethodPost, "/api/listings", map[string]any{
		"conn_app_id": conn.ID, "title": "Integration shop", "hints": true,
	}, &l))
	assert.Equal(t, "cash_order", l.Type)
	return hubID, conn.ID, l, admin, conn.ConnectionString
}

// I-70 (Order half): an Order connection listed from the admin API is on the
// dev relay, parses, names the signed cashorder1 string and its kinds.
func TestSpecListing_I70_OrderPublish(t *testing.T) {
	cfg := requireConfig(t)
	_, _, l, admin, connStr := listedOrderShop(t, cfg, "listed order shop")
	t.Cleanup(func() { _ = admin.doBody(http.MethodPost, fmt.Sprintf("/api/listings/%d/unlist", l.ID), nil, nil) })
	parsed, ev := listingOnRelay(t, cfg, l.Address)
	require.NotNil(t, parsed)
	assert.Equal(t, l.LastEventID, ev.ID)
	require.NotNil(t, parsed.Order, "a cash_order listing carries its Order connection")
	assert.Equal(t, parsed.NodeIdentity, parsed.Order.HubIdentity)
	assert.Contains(t, parsed.Summary, nmlisting.SummaryLightning)
	assert.Contains(t, parsed.Summary, nmlisting.SummaryOnchain)
	oc, err := coclient.Connect(ctxT(t), connStr, coclient.Options{})
	require.NoError(t, err)
	defer oc.Close()
	assert.Equal(t, oc.Connection().Pubkey, parsed.D)
}

// I-71 (retire) and I-RC2: retiring the Order connection answers get_info with
// "cash_order": {} first, then the closed version is on the relay; deleting a
// Cash Hub with a listed Order connection closes its listing too.
func TestSpecListing_I71_OrderRetireAndHubDelete(t *testing.T) {
	cfg := requireConfig(t)
	hubID, connID, l, admin, connStr := listedOrderShop(t, cfg, "retired order shop")
	time.Sleep(1100 * time.Millisecond)
	require.NoError(t, admin.do(http.MethodPost, fmt.Sprintf("/api/cash-hubs/%d/order-connections/%d/retire", hubID, connID), nil))
	oc, err := coclient.Connect(ctxT(t), connStr, coclient.Options{})
	require.NoError(t, err)
	defer oc.Close()
	terms, err := oc.Terms(ctxT(t))
	require.NoError(t, err)
	assert.True(t, terms.SellsNothing(), "retired: get_info answers cash_order {} at once")
	require.Eventually(t, func() bool {
		_, ev := listingOnRelay(t, cfg, l.Address)
		return eventTag(ev, "status") == "closed"
	}, 60*time.Second, 2*time.Second, "the closed version follows the retirement")

	// I-RC2: a Cash Hub with a listed Order connection, deleted.
	hub2, _, l2, admin2, _ := listedOrderShop(t, cfg, "deleted order shop")
	time.Sleep(1100 * time.Millisecond)
	require.NoError(t, admin2.deleteApp(hub2))
	require.Eventually(t, func() bool {
		_, ev := listingOnRelay(t, cfg, l2.Address)
		return eventTag(ev, "status") == "closed"
	}, 60*time.Second, 2*time.Second, "the deleted Cash Hub's listing is closed")
	time.Sleep(3 * time.Second)
	_, ev := listingOnRelay(t, cfg, l2.Address)
	assert.Equal(t, "closed", eventTag(ev, "status"), "no open version after the delete")
}
