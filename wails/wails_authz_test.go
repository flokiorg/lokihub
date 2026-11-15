package wails

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/tests/mocks"
)

// authzStubAPI records the fund-moving / seed-export calls it receives, so a
// test can assert the desktop gate blocked a route before it reached the API.
// Methods left unembedded panic if reached, which is itself a useful signal.
type authzStubAPI struct {
	api.API
	calls    []string
	nodeSeed string
}

func (a *authzStubAPI) GetMnemonic(string) (*api.MnemonicResponse, error) {
	a.calls = append(a.calls, "GetMnemonic")
	return &api.MnemonicResponse{Mnemonic: a.nodeSeed}, nil
}

func (a *authzStubAPI) GetBalances(context.Context) (*api.BalancesResponse, error) {
	a.calls = append(a.calls, "GetBalances")
	return &api.BalancesResponse{}, nil
}

func (a *authzStubAPI) SendPayment(context.Context, string, *uint64, *uint, map[string]interface{}) (*api.SendPaymentResponse, error) {
	a.calls = append(a.calls, "SendPayment")
	return &api.SendPaymentResponse{}, nil
}

func (a *authzStubAPI) Transfer(context.Context, *uint, *uint, uint64) error {
	a.calls = append(a.calls, "Transfer")
	return nil
}

func (a *authzStubAPI) OpenChannel(context.Context, *api.OpenChannelRequest) (*api.OpenChannelResponse, error) {
	a.calls = append(a.calls, "OpenChannel")
	return &api.OpenChannelResponse{}, nil
}

func (a *authzStubAPI) RedeemOnchainFunds(context.Context, string, uint64, *uint64, bool) (*api.RedeemOnchainFundsResponse, error) {
	a.calls = append(a.calls, "RedeemOnchainFunds")
	return &api.RedeemOnchainFundsResponse{}, nil
}

// fundMovingRoutes are the full-access routes a desktop session without the
// "full" tier must be refused (LOKIHUB-0043), with the node seed export the
// worst single instance (LOKIHUB-0042).
var fundMovingRoutes = []struct{ route, method string }{
	{"/api/payments/lnbc1", "POST"},
	{"/api/transfers", "POST"},
	{"/api/channels", "POST"},
	{"/api/wallet/redeem-onchain-funds", "POST"},
	{"/api/mnemonic", "POST"},
	{"/api/command", "POST"},
}

// A readonly desktop session is refused every fund-moving route and the node
// seed, and the API is never reached — mirroring the HTTP requireFullAccess 403.
func TestWailsRouter_ReadonlySessionRefusesFundRoutes(t *testing.T) {
	stub := &authzStubAPI{nodeSeed: "abandon abandon abandon"}
	app := &WailsApp{api: stub, ctx: context.Background()}
	app.session.markUnlocked("readonly")

	for _, r := range fundMovingRoutes {
		res := app.WailsRequestRouter(r.route, r.method, "{}")
		assert.Equal(t, errFullAccessRequired, res.Error, r.route)
		assert.Nil(t, res.Body, r.route)
	}
	assert.Empty(t, stub.calls, "no fund-moving route reached the API on a readonly session")
}

// A locked desktop app (session dropped on /api/stop) refuses fund ops even
// though node state may linger in memory.
func TestWailsRouter_LockedSessionRefusesFundRoutes(t *testing.T) {
	stub := &authzStubAPI{nodeSeed: "abandon abandon abandon"}
	app := &WailsApp{api: stub, ctx: context.Background()}
	app.session.markUnlocked("full")
	app.session.markLocked() // the desktop lock action

	for _, r := range fundMovingRoutes {
		res := app.WailsRequestRouter(r.route, r.method, "{}")
		assert.Equal(t, errFullAccessRequired, res.Error, r.route)
	}
	assert.Empty(t, stub.calls, "no fund-moving route reached the API on a locked session")
}

// A full-access desktop session reaches the node seed export, so the gate is
// additive and does not break the legitimate flow.
func TestWailsRouter_FullSessionExportsNodeSeed(t *testing.T) {
	stub := &authzStubAPI{nodeSeed: "abandon abandon abandon"}
	app := &WailsApp{api: stub, ctx: context.Background()}
	app.session.markUnlocked("full")

	res := app.WailsRequestRouter("/api/mnemonic", "POST", "{}")
	assert.Empty(t, res.Error)
	assert.Equal(t, api.MnemonicResponse{Mnemonic: "abandon abandon abandon"}, res.Body)
	assert.Equal(t, []string{"GetMnemonic"}, stub.calls)
}

// A readonly session keeps its read access: readonly routes are left ungated,
// so the fix does not regress them.
func TestWailsRouter_ReadonlySessionKeepsReadAccess(t *testing.T) {
	stub := &authzStubAPI{}
	app := &WailsApp{api: stub, ctx: context.Background()}
	app.session.markUnlocked("readonly")

	res := app.WailsRequestRouter("/api/balances", "GET", "")
	assert.Empty(t, res.Error)
	assert.Equal(t, []string{"GetBalances"}, stub.calls)
}

// With no explicit router session, a running node (e.g. auto-unlock started it
// at launch before any renderer unlock) is treated as a full-access session.
func TestWailsRouter_AutoUnlockRunningNodeHasFullAccess(t *testing.T) {
	stub := &authzStubAPI{nodeSeed: "abandon abandon abandon"}
	mockSvc := mocks.NewMockService(t)
	mockSvc.On("GetLNClient").Return(mocks.NewMockLNClient(t))

	app := &WailsApp{api: stub, svc: mockSvc, ctx: context.Background()}
	// No markUnlocked / markLocked: the session falls back to node-running state.

	res := app.WailsRequestRouter("/api/mnemonic", "POST", "{}")
	assert.Empty(t, res.Error)
	assert.Equal(t, api.MnemonicResponse{Mnemonic: "abandon abandon abandon"}, res.Body)
}

// routeRequiresFullAccess mirrors the HTTP auth tiers: public and readonly
// routes stay ungated; every other /api route is full-access by default.
func TestRouteRequiresFullAccess(t *testing.T) {
	app := &WailsApp{}
	cases := []struct {
		route, method string
		full          bool
	}{
		// public — must stay reachable without a session
		{"/api/unlock", "POST", false},
		{"/api/start", "POST", false},
		{"/api/backup", "POST", false},
		{"/api/restore", "POST", false},
		{"/api/info", "GET", false},
		{"/api/setup/config", "GET", false},
		{"/api/logout", "GET", false},
		{"/logout", "GET", false},
		// readonly
		{"/api/balances", "GET", false},
		{"/api/node/status", "GET", false},
		{"/api/apps/7", "GET", false},
		{"/api/apps/02abcd", "GET", false},
		{"/api/transactions/deadbeef", "GET", false},
		{"/api/circle/avatar-proxy?url=x", "GET", false},
		{"/api/invoices/estimate-fee", "POST", false},
		// full-access (fund-moving / mutating / sensitive)
		{"/api/payments/lnbc1", "POST", true},
		{"/api/transfers", "POST", true},
		{"/api/channels", "POST", true},
		{"/api/wallet/redeem-onchain-funds", "POST", true},
		{"/api/command", "POST", true},
		{"/api/apps", "POST", true},
		{"/api/apps/7/cash-stats", "GET", true},
		{"/api/listings", "GET", true},
		{"/api/cash-withdraw/node", "PUT", true},
		{"/api/mnemonic", "POST", true},
		{"/api/stop", "POST", true},
	}
	for _, c := range cases {
		require.Equal(t, c.full, app.routeRequiresFullAccess(c.route, c.method),
			"%s %s", c.method, c.route)
	}
}
