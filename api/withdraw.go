package api

import (
	"errors"

	"github.com/flokiorg/lokihub/cashwithdraw"
)

// ErrWithdrawUnavailable: the on-chain withdrawal service is not running.
var ErrWithdrawUnavailable = errors.New("on-chain withdrawals are not available")

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
