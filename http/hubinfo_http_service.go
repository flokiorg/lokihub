package http

import (
	"fmt"
	"net/http"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/hubinfo"
	"github.com/flokiorg/lokihub/service"
	"github.com/labstack/echo/v4"
)

type HubInfoHttpService struct {
	hubInfoSvc hubinfo.HubInfoService

	appConfig *config.AppConfig
	svc       service.Service
}

func NewHubInfoHttpService(svc service.Service, hubInfoSvc hubinfo.HubInfoService, appConfig *config.AppConfig) *HubInfoHttpService {
	return &HubInfoHttpService{
		hubInfoSvc: hubInfoSvc,

		appConfig: appConfig,
		svc:       svc,
	}
}

func (hubInfoHttpSvc *HubInfoHttpService) RegisterSharedRoutes(readOnlyApiGroup *echo.Group, fullAccessApiGroup *echo.Group, e *echo.Echo) {
	e.GET("/api/hub/info", hubInfoHttpSvc.hubInfoHandler)
	e.GET("/api/rates", hubInfoHttpSvc.fiatRateHandler)
	e.GET("/api/currencies", hubInfoHttpSvc.currenciesHandler)
	e.GET("/api/hub/faq", hubInfoHttpSvc.faqHandler)
}

func (hubInfoHttpSvc *HubInfoHttpService) hubInfoHandler(c echo.Context) error {
	info, err := hubInfoHttpSvc.hubInfoSvc.GetInfo(c.Request().Context())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to request loki info endpoint")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to request loki info endpoint: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, info)
}

func (hubInfoHttpSvc *HubInfoHttpService) faqHandler(c echo.Context) error {
	faq, err := hubInfoHttpSvc.hubInfoSvc.GetFAQ(c.Request().Context())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch FAQ")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to fetch FAQ: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, faq)
}

func (hubInfoHttpSvc *HubInfoHttpService) fiatRateHandler(c echo.Context) error {
	rate, err := hubInfoHttpSvc.hubInfoSvc.GetFiatRate(c.Request().Context())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to get Flokicoin rate")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to get Flokicoin rate: %s", err.Error()),
		})
	}
	return c.JSON(http.StatusOK, rate)
}

func (hubInfoHttpSvc *HubInfoHttpService) currenciesHandler(c echo.Context) error {
	currencies, err := hubInfoHttpSvc.hubInfoSvc.GetCurrencies(c.Request().Context())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to get currencies")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to get currencies: %s", err.Error()),
		})
	}
	return c.JSON(http.StatusOK, currencies)
}
