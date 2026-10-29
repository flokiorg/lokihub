package wails

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/cashwithdraw"
)

type withdrawStubAPI struct {
	api.API
	calls []string
}

func (a *withdrawStubAPI) GetCashHubWithdrawTerms(id uint) (*cashwithdraw.TermsView, error) {
	a.calls = append(a.calls, fmt.Sprintf("get terms %d", id))
	return &cashwithdraw.TermsView{}, nil
}

func (a *withdrawStubAPI) SetCashHubWithdrawTerms(id uint, in cashwithdraw.TermsInput) (*cashwithdraw.TermsView, error) {
	a.calls = append(a.calls, fmt.Sprintf("put terms %d %v %d %d", id, in.Enabled, in.FeeBaseMillis, in.FeePpm))
	return &cashwithdraw.TermsView{TermsInput: in}, nil
}

func (a *withdrawStubAPI) CashWithdrawNode() (*api.CashWithdrawNodeState, error) {
	a.calls = append(a.calls, "get node")
	return &api.CashWithdrawNodeState{}, nil
}

func (a *withdrawStubAPI) SetCashWithdrawNodeEnabled(on bool) error {
	a.calls = append(a.calls, fmt.Sprintf("put node %v", on))
	return nil
}

func (a *withdrawStubAPI) ListCashHubPayouts(id uint) ([]cashwithdraw.PayoutRow, error) {
	a.calls = append(a.calls, fmt.Sprintf("payouts %d", id))
	return nil, nil
}

func (a *withdrawStubAPI) GetCashHubPayout(id, pid uint) (*cashwithdraw.PayoutDetail, error) {
	a.calls = append(a.calls, fmt.Sprintf("payout %d %d", id, pid))
	return &cashwithdraw.PayoutDetail{}, nil
}

func (a *withdrawStubAPI) CashWithdrawStatus() (*cashwithdraw.EngineStatus, error) {
	a.calls = append(a.calls, "status")
	return &cashwithdraw.EngineStatus{}, nil
}

// The desktop app serves the on-chain withdrawal routes the HTTP server
// serves, and refuses a malformed hub id.
func TestWailsRouter_Withdraw(t *testing.T) {
	stub := &withdrawStubAPI{}
	app := &WailsApp{api: stub, ctx: context.Background()}
	for _, r := range []struct{ route, method, body string }{
		{"/api/cash-withdraw/node", "GET", ""},
		{"/api/cash-withdraw/node", "PUT", `{"enabled":true}`},
		{"/api/cash-withdraw/status", "GET", ""},
		{"/api/cash-hubs/7/withdraw-terms", "GET", ""},
		{"/api/cash-hubs/7/withdraw-terms", "PUT", `{"enabled":true,"fee_base_millis":500000,"fee_ppm":2000}`},
		{"/api/cash-hubs/7/payouts", "GET", ""},
		{"/api/cash-hubs/7/payouts/3", "GET", ""},
	} {
		res := app.WailsRequestRouter(r.route, r.method, r.body)
		assert.Empty(t, res.Error, r.route)
	}
	assert.Equal(t, []string{"get node", "put node true", "status", "get terms 7", "put terms 7 true 500000 2000", "payouts 7", "payout 7 3"}, stub.calls)

	res := app.WailsRequestRouter("/api/cash-hubs/0/withdraw-terms", "GET", "")
	assert.Equal(t, "invalid id", res.Error)
	res = app.WailsRequestRouter("/api/cash-hubs/7/payouts/0", "GET", "")
	assert.Equal(t, "invalid id", res.Error)
	res = app.WailsRequestRouter("/api/cash-withdraw/node", "PUT", `{`)
	assert.NotEmpty(t, res.Error)
	assert.Len(t, stub.calls, 7, "nothing called for a refused request")
}
