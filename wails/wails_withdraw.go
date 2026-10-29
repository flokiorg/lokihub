package wails

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/cashwithdraw"
)

var errInvalidHubID = errors.New("invalid id")

var (
	withdrawTermsRouteRegex   = regexp.MustCompile(`^/api/cash-hubs/([0-9]+)/withdraw-terms$`)
	withdrawPayoutsRouteRegex = regexp.MustCompile(`^/api/cash-hubs/([0-9]+)/payouts$`)
	withdrawPayoutRouteRegex  = regexp.MustCompile(`^/api/cash-hubs/([0-9]+)/payouts/([0-9]+)$`)
)

// routeWithdraw mirrors the HTTP on-chain withdrawal routes (WP-23); ok is
// false for a route it does not serve.
func (app *WailsApp) routeWithdraw(route, method, body string) (WailsRequestRouterResponse, bool) {
	w, ok := app.api.(api.WithdrawAPI)
	if !ok {
		return WailsRequestRouterResponse{}, false
	}
	respond := func(v any, err error) (WailsRequestRouterResponse, bool) {
		if err != nil {
			return WailsRequestRouterResponse{Body: nil, Error: err.Error()}, true
		}
		return WailsRequestRouterResponse{Body: v, Error: ""}, true
	}
	switch {
	case route == "/api/cash-withdraw/status" && method == "GET":
		return respond(w.CashWithdrawStatus())
	case route == "/api/cash-withdraw/node" && method == "GET":
		return respond(w.CashWithdrawNode())
	case route == "/api/cash-withdraw/node" && method == "PUT":
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal([]byte(body), &in); err != nil {
			return respond(nil, err)
		}
		return respond(nil, w.SetCashWithdrawNodeEnabled(in.Enabled))
	}
	if m := withdrawTermsRouteRegex.FindStringSubmatch(route); m != nil {
		id, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil || id == 0 {
			return respond(nil, errInvalidHubID)
		}
		switch method {
		case "GET":
			return respond(w.GetCashHubWithdrawTerms(uint(id)))
		case "PUT":
			var in cashwithdraw.TermsInput
			if err := json.Unmarshal([]byte(body), &in); err != nil {
				return respond(nil, err)
			}
			return respond(w.SetCashHubWithdrawTerms(uint(id), in))
		}
	}
	if m := withdrawPayoutsRouteRegex.FindStringSubmatch(route); m != nil && method == "GET" {
		id, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil || id == 0 {
			return respond(nil, errInvalidHubID)
		}
		return respond(w.ListCashHubPayouts(uint(id)))
	}
	if m := withdrawPayoutRouteRegex.FindStringSubmatch(route); m != nil && method == "GET" {
		id, err := strconv.ParseUint(m[1], 10, 64)
		pid, perr := strconv.ParseUint(m[2], 10, 64)
		if err != nil || perr != nil || id == 0 || pid == 0 {
			return respond(nil, errInvalidHubID)
		}
		return respond(w.GetCashHubPayout(uint(id), uint(pid)))
	}
	return WailsRequestRouterResponse{}, false
}
