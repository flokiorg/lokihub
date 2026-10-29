package http

import (
	"errors"
	"net/http"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/lsps/discovery"
	"github.com/labstack/echo/v4"
)

// lspStatus maps a catalogue error to its HTTP status.
func lspStatus(err error) int {
	switch {
	case errors.Is(err, discovery.ErrRefused), errors.Is(err, discovery.ErrNoAnnouncement):
		return http.StatusBadRequest
	case errors.Is(err, api.ErrLSPCatalogueNotRunning):
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// listLSPsHandler is GET /api/lsps: the catalogue (WP-26).
func (httpSvc *HttpService) listLSPsHandler(c echo.Context) error {
	lsps, err := httpSvc.api.HandleListLSPs(c.Request().Context())
	if err != nil {
		return c.JSON(lspStatus(err), ErrorResponse{Message: err.Error()})
	}
	return c.JSON(http.StatusOK, lsps)
}

// addLSPHandler is POST /api/lsps {pubkey}: that node's announcement is
// fetched; refused when there is none (LP-4).
func (httpSvc *HttpService) addLSPHandler(c echo.Context) error {
	var req api.AddLSPRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
	}
	lsp, err := httpSvc.api.HandleAddLSP(c.Request().Context(), &req)
	if err != nil {
		return c.JSON(lspStatus(err), ErrorResponse{Message: err.Error()})
	}
	return c.JSON(http.StatusOK, lsp)
}

// trustLSPHandler is PUT /api/lsps/:pubkey/trust {trusted}; trusting runs the
// live check.
func (httpSvc *HttpService) trustLSPHandler(c echo.Context) error {
	var req api.TrustLSPRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
	}
	lsp, err := httpSvc.api.HandleTrustLSP(c.Request().Context(), c.Param("pubkey"), req.Trusted)
	if err != nil {
		return c.JSON(lspStatus(err), ErrorResponse{Message: err.Error()})
	}
	return c.JSON(http.StatusOK, lsp)
}

// checkLSPHandler is POST /api/lsps/:pubkey/check: the live check again.
func (httpSvc *HttpService) checkLSPHandler(c echo.Context) error {
	lsp, err := httpSvc.api.HandleLSPLiveCheck(c.Request().Context(), c.Param("pubkey"))
	if err != nil {
		return c.JSON(lspStatus(err), ErrorResponse{Message: err.Error()})
	}
	return c.JSON(http.StatusOK, lsp)
}
