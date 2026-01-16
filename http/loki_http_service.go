package http

import (
	"fmt"
	"net/http"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/loki"
	"github.com/flokiorg/lokihub/service"
	"github.com/labstack/echo/v4"
)

type LokiHttpService struct {
	lokiSvc loki.LokiService

	appConfig *config.AppConfig
	svc       service.Service
}

func NewLokiHttpService(svc service.Service, lokiSvc loki.LokiService, appConfig *config.AppConfig) *LokiHttpService {
	return &LokiHttpService{
		lokiSvc: lokiSvc,

		appConfig: appConfig,
		svc:       svc,
	}
}

func (lokiHttpSvc *LokiHttpService) RegisterSharedRoutes(readOnlyApiGroup *echo.Group, fullAccessApiGroup *echo.Group, e *echo.Echo) {
	e.GET("/api/loki/info", lokiHttpSvc.lokiInfoHandler)
	e.GET("/api/loki/rates", lokiHttpSvc.lokiFlokicoinRateHandler)
	e.GET("/api/currencies", lokiHttpSvc.lokiCurrenciesHandler)
	e.GET("/api/loki/faq", lokiHttpSvc.lokiFAQHandler)
}

func (lokiHttpSvc *LokiHttpService) lokiInfoHandler(c echo.Context) error {
	info, err := lokiHttpSvc.lokiSvc.GetInfo(c.Request().Context())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to request loki info endpoint")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to request loki info endpoint: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, info)
}

func (lokiHttpSvc *LokiHttpService) lokiFAQHandler(c echo.Context) error {
	faq, err := lokiHttpSvc.lokiSvc.GetFAQ(c.Request().Context())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch FAQ")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to fetch FAQ: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, faq)
}

func (lokiHttpSvc *LokiHttpService) lokiFlokicoinRateHandler(c echo.Context) error {
	rate, err := lokiHttpSvc.lokiSvc.GetFlokicoinRate(c.Request().Context())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to get Flokicoin rate")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to get Flokicoin rate: %s", err.Error()),
		})
	}
	return c.JSON(http.StatusOK, rate)
}

func (lokiHttpSvc *LokiHttpService) lokiCurrenciesHandler(c echo.Context) error {
	currencies, err := lokiHttpSvc.lokiSvc.GetCurrencies(c.Request().Context())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to get currencies")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to get currencies: %s", err.Error()),
		})
	}
	return c.JSON(http.StatusOK, currencies)
}
