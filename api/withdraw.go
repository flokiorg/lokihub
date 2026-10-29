package api

import (
	"errors"

	"github.com/flokiorg/lokihub/cashwithdraw"
)

// ErrWithdrawUnavailable: the on-chain withdrawal service is not running.
var ErrWithdrawUnavailable = errors.New("on-chain withdrawals are not available")

// WithdrawAPI is the on-chain withdrawal admin API (WP-23), served by the HTTP
// and desktop routers.
type WithdrawAPI interface {
	GetCashHubWithdrawTerms(hubAppID uint) (*cashwithdraw.TermsView, error)
	SetCashHubWithdrawTerms(hubAppID uint, in cashwithdraw.TermsInput) (*cashwithdraw.TermsView, error)
	CashWithdrawNode() (*CashWithdrawNodeState, error)
	SetCashWithdrawNodeEnabled(on bool) error
	ListCashHubPayouts(hubAppID uint) ([]cashwithdraw.PayoutRow, error)
	GetCashHubPayout(hubAppID, payoutID uint) (*cashwithdraw.PayoutDetail, error)
	CashWithdrawStatus() (*cashwithdraw.EngineStatus, error)
}

// CashWithdrawNodeState is the node switch and whether this node can pay
// on-chain withdrawals at all (the switch is refused on when it cannot).
type CashWithdrawNodeState struct {
	Enabled bool `json:"enabled"`
	Capable bool `json:"capable"`
}

var _ WithdrawAPI = (*api)(nil)

// CashWithdrawNode reads the node switch.
func (api *api) CashWithdrawNode() (*CashWithdrawNodeState, error) {
	w, err := api.withdrawService()
	if err != nil {
		return nil, err
	}
	return &CashWithdrawNodeState{Enabled: w.NodeEnabled(), Capable: w.Capable()}, nil
}

func (api *api) withdrawService() (*cashwithdraw.Service, error) {
	w, ok := api.svc.(interface {
		GetWithdrawService() *cashwithdraw.Service
	})
	if !ok || w.GetWithdrawService() == nil {
		return nil, ErrWithdrawUnavailable
	}
	return w.GetWithdrawService(), nil
}

// GetCashHubWithdrawTerms is a Cash Hub's withdrawal terms with the derived
// values (WP-23).
func (api *api) GetCashHubWithdrawTerms(hubAppID uint) (*cashwithdraw.TermsView, error) {
	w, err := api.withdrawService()
	if err != nil {
		return nil, err
	}
	return w.GetTerms(hubAppID)
}

// SetCashHubWithdrawTerms replaces a Cash Hub's withdrawal terms; a refused
// value is constants.ErrInvalidParams, never clamped.
func (api *api) SetCashHubWithdrawTerms(hubAppID uint, in cashwithdraw.TermsInput) (*cashwithdraw.TermsView, error) {
	w, err := api.withdrawService()
	if err != nil {
		return nil, err
	}
	return w.PutTerms(hubAppID, in)
}

// SetCashWithdrawNodeEnabled sets the node switch.
func (api *api) SetCashWithdrawNodeEnabled(on bool) error {
	w, err := api.withdrawService()
	if err != nil {
		return err
	}
	return w.SetNodeEnabled(on)
}

// ListCashHubPayouts lists a Cash Hub's payouts.
func (api *api) ListCashHubPayouts(hubAppID uint) ([]cashwithdraw.PayoutRow, error) {
	w, err := api.withdrawService()
	if err != nil {
		return nil, err
	}
	return w.ListPayouts(hubAppID, 500)
}

// GetCashHubPayout is one payout with its versions and archive facts.
func (api *api) GetCashHubPayout(hubAppID, payoutID uint) (*cashwithdraw.PayoutDetail, error) {
	w, err := api.withdrawService()
	if err != nil {
		return nil, err
	}
	return w.GetPayout(hubAppID, payoutID)
}

// CashWithdrawStatus is the payout engine's health.
func (api *api) CashWithdrawStatus() (*cashwithdraw.EngineStatus, error) {
	e, ok := api.svc.(interface{ GetWithdrawEngine() *cashwithdraw.Engine })
	if !ok || e.GetWithdrawEngine() == nil {
		return nil, ErrWithdrawUnavailable
	}
	return e.GetWithdrawEngine().Status()
}
