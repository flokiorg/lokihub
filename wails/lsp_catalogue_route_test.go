package wails

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/flokiorg/lokihub/api"
)

type lspCatalogueAPI struct {
	api.API
	calls []string
}

func (a *lspCatalogueAPI) HandleListLSPs(context.Context) ([]api.LSPCatalogueEntry, error) {
	a.calls = append(a.calls, "list")
	return []api.LSPCatalogueEntry{}, nil
}

func (a *lspCatalogueAPI) HandleAddLSP(_ context.Context, req *api.AddLSPRequest) (*api.LSPCatalogueEntry, error) {
	a.calls = append(a.calls, "add "+req.Pubkey)
	return &api.LSPCatalogueEntry{}, nil
}

func (a *lspCatalogueAPI) HandleTrustLSP(_ context.Context, key string, trusted bool) (*api.LSPCatalogueEntry, error) {
	if trusted {
		a.calls = append(a.calls, "trust "+key)
	} else {
		a.calls = append(a.calls, "untrust "+key)
	}
	return &api.LSPCatalogueEntry{}, nil
}

func (a *lspCatalogueAPI) HandleLSPLiveCheck(_ context.Context, key string) (*api.LSPCatalogueEntry, error) {
	a.calls = append(a.calls, "check "+key)
	return &api.LSPCatalogueEntry{}, nil
}

// U-26.12: the desktop app serves the catalogue routes the HTTP server serves.
func TestWailsRouter_LSPCatalogue(t *testing.T) {
	const key = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	stub := &lspCatalogueAPI{}
	app := &WailsApp{api: stub, ctx: context.Background()}
	for _, r := range []struct{ route, method, body string }{
		{"/api/lsps", "GET", ""},
		{"/api/lsps", "POST", `{"pubkey":"` + key + `"}`},
		{"/api/lsps/" + key + "/trust", "PUT", `{"trusted":true}`},
		{"/api/lsps/" + key + "/trust", "PUT", `{"trusted":false}`},
		{"/api/lsps/" + key + "/check", "POST", ""},
	} {
		res := app.WailsRequestRouter(r.route, r.method, r.body)
		assert.Empty(t, res.Error, r.route)
	}
	assert.Equal(t, []string{"list", "add " + key, "trust " + key, "untrust " + key, "check " + key}, stub.calls)
}
