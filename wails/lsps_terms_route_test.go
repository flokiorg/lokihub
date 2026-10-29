package wails

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/lsps/manager"
)

type purchaseStubAPI struct {
	api.API
	err error
}

func (a *purchaseStubAPI) LSPS2Buy(context.Context, *api.LSPS2BuyRequest) (*api.LSPS2BuyResponse, error) {
	return nil, a.err
}

func (a *purchaseStubAPI) LSPS1CreateOrder(context.Context, *api.LSPS1CreateOrderRequest) (interface{}, error) {
	return nil, a.err
}

// A purchase refused for terms worse than announced answers the desktop app
// with the HTTP 409 body (message, announced and live terms), on both purchase
// routes; any other refusal carries its message only.
func TestWailsRouter_PurchaseTermsRefusalCarriesTheTerms(t *testing.T) {
	terms := manager.Terms{Worse: true, Fields: []manager.TermField{
		{Field: "lsps2_fee[0].min_fee", Announced: "2000000", Live: "3000000", Verdict: "worse"}}}
	routes := []struct{ route, body string }{
		{"/api/lsps2/buy", `{"lspPubkey":"x","paymentSizeMloki":1000}`},
		{"/api/lsps1/order", `{"lsp_pubkey":"x","amount_loki":1000}`},
	}
	for _, r := range routes {
		stub := &purchaseStubAPI{err: fmt.Errorf("buy: %w", &manager.TermsWorseError{Terms: terms})}
		app := &WailsApp{api: stub, ctx: context.Background()}
		res := app.WailsRequestRouter(r.route, "POST", r.body)
		require.Contains(t, res.Error, "worse than it announced", r.route)
		refusal, ok := res.Body.(*manager.TermsRefusal)
		require.True(t, ok, r.route)
		assert.Equal(t, terms, refusal.Terms)
		assert.Equal(t, res.Error, refusal.Message)

		stub.err = errors.New("LSP returned error: no")
		res = app.WailsRequestRouter(r.route, "POST", r.body)
		assert.Equal(t, "LSP returned error: no", res.Error)
		assert.Nil(t, res.Body, r.route)
	}
}
