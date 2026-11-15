package wails

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
)

var (
	listingRouteRegex       = regexp.MustCompile(`^/api/listings/([0-9]+)$`)
	listingActionRouteRegex = regexp.MustCompile(`^/api/listings/([0-9]+)/(pause|resume|unlist|revoke-connection|check-operator|preview)$`)
	cashHubRevokeRouteRegex = regexp.MustCompile(`^/api/cash-hubs/([0-9]+)/revoke-connection$`)
)

// routeListings mirrors the HTTP offer-listing routes (WP-25); ok is false for a
// route it does not serve.
func (app *WailsApp) routeListings(ctx context.Context, route, method, body string) (WailsRequestRouterResponse, bool) {
	respond := func(v any, err error) (WailsRequestRouterResponse, bool) {
		if err != nil {
			return WailsRequestRouterResponse{Body: nil, Error: err.Error()}, true
		}
		return WailsRequestRouterResponse{Body: v, Error: ""}, true
	}
	switch {
	case route == "/api/listings" && method == "GET":
		return respond(app.api.ListListings(ctx))
	case route == "/api/listings" && method == "POST":
		return respond(app.api.CreateListing(ctx, []byte(body)))
	case route == "/api/listing-relays" && method == "GET":
		return respond(app.api.GetListingRelays(), nil)
	case route == "/api/listing-relays" && method == "PUT":
		var req struct {
			Relays []string `json:"relays"`
		}
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			return respond(nil, err)
		}
		return respond(app.api.SetListingRelays(ctx, req.Relays))
	}
	if m := listingRouteRegex.FindStringSubmatch(route); m != nil && method == "PUT" {
		id, _ := strconv.ParseUint(m[1], 10, 64)
		return respond(app.api.UpdateListing(ctx, uint(id), []byte(body)))
	}
	if m := listingActionRouteRegex.FindStringSubmatch(route); m != nil {
		id, _ := strconv.ParseUint(m[1], 10, 64)
		if m[2] == "preview" && method == "GET" {
			return respond(app.api.PreviewListing(ctx, uint(id)))
		}
		if m[2] != "preview" && method == "POST" {
			return respond(app.api.ListingAction(ctx, uint(id), m[2]))
		}
	}
	if m := cashHubRevokeRouteRegex.FindStringSubmatch(route); m != nil && method == "POST" {
		id, _ := strconv.ParseUint(m[1], 10, 64)
		return respond(app.api.RegenerateCashHubConnection(ctx, uint(id)))
	}
	return WailsRequestRouterResponse{}, false
}
