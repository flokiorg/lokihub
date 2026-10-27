package http

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/listing"
)

// registerListingRoutes adds the offer-listing admin API (WP-25) to the
// full-access group.
func (httpSvc *HttpService) registerListingRoutes(g *echo.Group) {
	g.GET("/listings", httpSvc.listingsListHandler)
	g.POST("/listings", httpSvc.listingsCreateHandler)
	g.PUT("/listings/:id", httpSvc.listingsUpdateHandler)
	g.GET("/listings/:id/preview", httpSvc.listingsPreviewHandler)
	for _, action := range []string{"pause", "resume", "unlist", "revoke-connection", "check-operator"} {
		g.POST("/listings/:id/"+action, httpSvc.listingsActionHandler(action))
	}
	g.GET("/listing-relays", httpSvc.listingRelaysGetHandler)
	g.PUT("/listing-relays", httpSvc.listingRelaysSetHandler)
	g.POST("/cash-hubs/:id/revoke-connection", httpSvc.cashHubRevokeConnectionHandler)
}

// listingStatus maps a listing error to its HTTP status: 400 for a refused
// target or field, 404 for no such listing, 503 when the node cannot sign or is
// not started.
func listingStatus(err error) int {
	switch {
	case errors.Is(err, listing.ErrRefused):
		return http.StatusBadRequest
	case errors.Is(err, listing.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, listing.ErrCannotSign), errors.Is(err, api.ErrNodeNotStarted):
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func listingError(c echo.Context, err error) error {
	return c.JSON(listingStatus(err), ErrorResponse{Message: err.Error()})
}

func listingID(c echo.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, c.JSON(http.StatusBadRequest, ErrorResponse{Message: "invalid id"})
	}
	return uint(id), nil
}

func (httpSvc *HttpService) listingsListHandler(c echo.Context) error {
	views, err := httpSvc.api.ListListings(c.Request().Context())
	if err != nil {
		return listingError(c, err)
	}
	return c.JSON(http.StatusOK, views)
}

func (httpSvc *HttpService) listingsCreateHandler(c echo.Context) error {
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, 1<<16))
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
	}
	view, err := httpSvc.api.CreateListing(c.Request().Context(), body)
	if err != nil {
		return listingError(c, err)
	}
	return c.JSON(http.StatusOK, view)
}

func (httpSvc *HttpService) listingsUpdateHandler(c echo.Context) error {
	id, err := listingID(c)
	if id == 0 {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(c.Request().Body, 1<<16))
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
	}
	view, err := httpSvc.api.UpdateListing(c.Request().Context(), id, body)
	if err != nil {
		return listingError(c, err)
	}
	return c.JSON(http.StatusOK, view)
}

func (httpSvc *HttpService) listingsPreviewHandler(c echo.Context) error {
	id, err := listingID(c)
	if id == 0 {
		return err
	}
	ev, err := httpSvc.api.PreviewListing(c.Request().Context(), id)
	if err != nil {
		return listingError(c, err)
	}
	return c.JSON(http.StatusOK, ev)
}

func (httpSvc *HttpService) listingsActionHandler(action string) echo.HandlerFunc {
	return func(c echo.Context) error {
		id, err := listingID(c)
		if id == 0 {
			return err
		}
		view, err := httpSvc.api.ListingAction(c.Request().Context(), id, action)
		if err != nil {
			return listingError(c, err)
		}
		return c.JSON(http.StatusOK, view)
	}
}

func (httpSvc *HttpService) listingRelaysGetHandler(c echo.Context) error {
	return c.JSON(http.StatusOK, httpSvc.api.GetListingRelays())
}

func (httpSvc *HttpService) listingRelaysSetHandler(c echo.Context) error {
	var body struct {
		Relays []string `json:"relays"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
	}
	resp, err := httpSvc.api.SetListingRelays(c.Request().Context(), body.Relays)
	if err != nil {
		return listingError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (httpSvc *HttpService) cashHubRevokeConnectionHandler(c echo.Context) error {
	id, err := listingID(c)
	if id == 0 {
		return err
	}
	resp, err := httpSvc.api.RegenerateCashHubConnection(c.Request().Context(), id)
	if err != nil {
		return listingError(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}
