package http

import (
	"net/http"

	"github.com/flokiorg/lokihub/api"
	"github.com/labstack/echo/v4"
)

func (httpSvc *HttpService) listLSPsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	lsps, err := httpSvc.api.HandleListLSPs(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Message: err.Error()})
	}
	return c.JSON(http.StatusOK, lsps)
}

func (httpSvc *HttpService) addLSPHandler(c echo.Context) error {
	var req api.AddLSPRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
	}
	ctx := c.Request().Context()
	lsp, err := httpSvc.api.HandleAddLSP(ctx, &req)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Message: err.Error()})
	}
	return c.JSON(http.StatusOK, lsp)
}

func (httpSvc *HttpService) updateLSPHandler(c echo.Context) error {
	pubkey := c.Param("pubkey")
	var req api.UpdateLSPRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
	}
	ctx := c.Request().Context()
	err := httpSvc.api.HandleUpdateLSP(ctx, pubkey, &req)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Message: err.Error()})
	}
	return c.NoContent(http.StatusOK)
}

func (httpSvc *HttpService) deleteLSPHandler(c echo.Context) error {
	pubkey := c.Param("pubkey")
	ctx := c.Request().Context()
	err := httpSvc.api.HandleDeleteLSP(ctx, pubkey)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Message: err.Error()})
	}
	return c.NoContent(http.StatusOK)
}
