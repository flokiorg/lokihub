package http

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/cashwithdraw"
	"github.com/flokiorg/lokihub/constants"
)

// withdrawAPI is the part of the API the on-chain withdrawal routes use.
type withdrawAPI interface {
	GetCashHubWithdrawTerms(hubAppID uint) (*cashwithdraw.TermsView, error)
	SetCashHubWithdrawTerms(hubAppID uint, in cashwithdraw.TermsInput) (*cashwithdraw.TermsView, error)
	SetCashWithdrawNodeEnabled(on bool) error
	ListCashHubPayouts(hubAppID uint) ([]cashwithdraw.PayoutRow, error)
}

// registerWithdrawRoutes adds the on-chain withdrawal admin API (WP-23).
func (httpSvc *HttpService) registerWithdrawRoutes(g *echo.Group) {
	w, ok := httpSvc.api.(withdrawAPI)
	if !ok {
		return
	}
	g.GET("/cash-hubs/:id/withdraw-terms", func(c echo.Context) error {
		id, err := hubID(c)
		if err != nil {
			return err
		}
		v, err := w.GetCashHubWithdrawTerms(id)
		if err != nil {
			return withdrawError(c, err)
		}
		return c.JSON(http.StatusOK, v)
	})
	g.PUT("/cash-hubs/:id/withdraw-terms", func(c echo.Context) error {
		id, err := hubID(c)
		if err != nil {
			return err
		}
		var in cashwithdraw.TermsInput
		if err := c.Bind(&in); err != nil {
			return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "invalid body"})
		}
		v, err := w.SetCashHubWithdrawTerms(id, in)
		if err != nil {
			return withdrawError(c, err)
		}
		return c.JSON(http.StatusOK, v)
	})
	g.GET("/cash-hubs/:id/payouts", func(c echo.Context) error {
		id, err := hubID(c)
		if err != nil {
			return err
		}
		rows, err := w.ListCashHubPayouts(id)
		if err != nil {
			return withdrawError(c, err)
		}
		return c.JSON(http.StatusOK, rows)
	})
	g.PUT("/cash-withdraw/node", func(c echo.Context) error {
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if err := c.Bind(&in); err != nil {
			return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "invalid body"})
		}
		if err := w.SetCashWithdrawNodeEnabled(in.Enabled); err != nil {
			return withdrawError(c, err)
		}
		return c.NoContent(http.StatusNoContent)
	})
}

func hubID(c echo.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, c.JSON(http.StatusBadRequest, ErrorResponse{Message: "invalid id"})
	}
	return uint(id), nil
}

func withdrawError(c echo.Context, err error) error {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, constants.ErrInvalidParams):
		status = http.StatusBadRequest
	case errors.Is(err, cashwithdraw.ErrNotCashHub):
		status = http.StatusNotFound
	case errors.Is(err, api.ErrWithdrawUnavailable):
		status = http.StatusServiceUnavailable
	}
	return c.JSON(status, ErrorResponse{Message: err.Error()})
}
