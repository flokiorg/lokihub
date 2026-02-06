package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	echojwt "github.com/labstack/echo-jwt/v4"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/config"
	lokidb "github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/pkg/appstore"
	"github.com/flokiorg/lokihub/service"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/frontend"
)

type authTokenResponse struct {
	Token string `json:"token"`
}

type jwtCustomClaims struct {
	// we can add extra claims here
	// Name  string `json:"name"`
	// Admin bool   `json:"admin"`
	Permission string `json:"permission,omitempty"` // "full" or "readonly"
	jwt.RegisteredClaims
}

type HttpService struct {
	api            api.API
	lokiHttpSvc    *LokiHttpService
	cfg            config.Config
	eventPublisher events.EventPublisher
	db             *gorm.DB
	appsSvc        apps.AppsService
	appStoreSvc    appstore.Service
}

func NewHttpService(svc service.Service, eventPublisher events.EventPublisher) *HttpService {
	return &HttpService{
		api:            api.NewAPI(svc, svc.GetDB(), svc.GetConfig(), svc.GetKeys(), svc.GetLokiSvc(), eventPublisher),
		lokiHttpSvc:    NewLokiHttpService(svc, svc.GetLokiSvc(), svc.GetConfig().GetEnv()),
		cfg:            svc.GetConfig(),
		eventPublisher: eventPublisher,
		db:             svc.GetDB(),
		appsSvc:        apps.NewAppsService(svc.GetDB(), eventPublisher, svc.GetKeys(), svc.GetConfig()),
		appStoreSvc:    svc.GetAppStoreSvc(),
	}
}

func (httpSvc *HttpService) RegisterSharedRoutes(e *echo.Echo) {
	e.HideBanner = true

	e.Use(middleware.SecureWithConfig(middleware.SecureConfig{
		ContentTypeNosniff:    "nosniff",
		XFrameOptions:         "DENY",
		ContentSecurityPolicy: "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' wss: ws://wails.localhost:*; img-src 'self' data:; frame-src 'none'; object-src 'none'; base-uri 'self';",
		ReferrerPolicy:        "no-referrer",
	}))
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI:       true,
		LogStatus:    true,
		LogRemoteIP:  true,
		LogUserAgent: true,
		LogHost:      true,
		LogRequestID: true,
		LogValuesFunc: func(c echo.Context, values middleware.RequestLoggerValues) error {
			logger.HttpLogger.Info().
				Str("uri", values.URI).
				Int("status", values.Status).
				Str("remote_ip", values.RemoteIP).
				Str("user_agent", values.UserAgent).
				Str("host", values.Host).
				Str("request_id", values.RequestID).
				Msg("handled API request")
			return nil
		},
	}))

	e.Use(middleware.Recover())
	e.Use(middleware.RequestID())

	e.GET("/api/info", httpSvc.infoHandler)
	e.GET("/api/setup/config", httpSvc.getServicesHandler)
	e.GET("/api/setup/status", httpSvc.setupStatusHandler)
	e.POST("/api/setup", httpSvc.setupHandler)
	e.POST("/api/setup/local", httpSvc.setupLocalHandler)
	e.POST("/api/setup/manual", httpSvc.setupManualHandler)
	e.POST("/api/restore", httpSvc.restoreBackupHandler)

	// Public app store routes
	e.GET("/api/appstore/logos/:appId", httpSvc.getAppStoreLogoHandler)

	// allow one unlock request per second
	unlockRateLimiter := middleware.RateLimiter(middleware.NewRateLimiterMemoryStore(1))
	e.POST("/api/start", httpSvc.startHandler, unlockRateLimiter)
	e.POST("/api/unlock", httpSvc.unlockHandler, unlockRateLimiter)
	e.POST("/api/backup", httpSvc.createBackupHandler, unlockRateLimiter)
	e.GET("/logout", httpSvc.logoutHandler, unlockRateLimiter)

	// Redirect /wallet/swap to /settings if swap is disabled
	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if c.Request().Method == http.MethodGet && (c.Path() == "/wallet/swap" || strings.HasPrefix(c.Request().URL.Path, "/wallet/swap")) {
				if !httpSvc.cfg.EnableSwap() {
					return c.Redirect(http.StatusTemporaryRedirect, "/settings")
				}
			}
			return next(c)
		}
	})

	frontend.RegisterHandlers(e)

	// restricted routes
	// Configure middleware with the custom claims type
	jwtConfig := echojwt.Config{
		NewClaimsFunc: func(c echo.Context) jwt.Claims {
			return new(jwtCustomClaims)
		},
		// use a custom key func as the JWT secret will change if the user changes their unlock password
		KeyFunc: func(token *jwt.Token) (interface{}, error) {
			secret, err := httpSvc.cfg.GetJWTSecret()
			if err != nil {
				return nil, err
			}
			return []byte(secret), nil
		},
		TokenLookup: "header:Authorization:Bearer ,query:token",
	}
	// Read-only API group - accessible to both full and readonly tokens
	readOnlyApiGroup := e.Group("/api")
	readOnlyApiGroup.Use(echojwt.WithConfig(jwtConfig))

	readOnlyApiGroup.GET("/apps", httpSvc.appsListHandler)
	readOnlyApiGroup.GET("/apps/:pubkey", httpSvc.appsShowByPubkeyHandler)
	readOnlyApiGroup.GET("/apps/:id", httpSvc.appsShowHandler)
	readOnlyApiGroup.GET("/channels", httpSvc.channelsListHandler)
	readOnlyApiGroup.POST("/invoices/estimate-fee", httpSvc.estimateInvoiceFeeHandler)

	readOnlyApiGroup.GET("/node/connection-info", httpSvc.nodeConnectionInfoHandler)
	readOnlyApiGroup.GET("/node/status", httpSvc.nodeStatusHandler)
	readOnlyApiGroup.GET("/node/network-graph", httpSvc.nodeNetworkGraphHandler)
	readOnlyApiGroup.GET("/node/transactions", httpSvc.listOnchainTransactionsHandler)
	readOnlyApiGroup.GET("/peers", httpSvc.listPeers)
	readOnlyApiGroup.GET("/wallet/address", httpSvc.onchainAddressHandler)
	readOnlyApiGroup.GET("/wallet/capabilities", httpSvc.capabilitiesHandler)
	readOnlyApiGroup.GET("/transactions", httpSvc.listTransactionsHandler)
	readOnlyApiGroup.GET("/transactions/:paymentHash", httpSvc.lookupTransactionHandler)
	readOnlyApiGroup.GET("/balances", httpSvc.balancesHandler)
	readOnlyApiGroup.GET("/mempool", httpSvc.mempoolApiHandler)
	readOnlyApiGroup.GET("/log/:type", httpSvc.getLogOutputHandler)
	readOnlyApiGroup.GET("/health", httpSvc.healthHandler)
	readOnlyApiGroup.GET("/commands", httpSvc.getCustomNodeCommandsHandler)
	readOnlyApiGroup.GET("/swaps", httpSvc.listSwapsHandler)
	readOnlyApiGroup.GET("/swaps/:swapId", httpSvc.lookupSwapHandler)
	readOnlyApiGroup.GET("/swaps/out/info", httpSvc.getSwapOutInfoHandler)
	readOnlyApiGroup.GET("/swaps/in/info", httpSvc.getSwapInInfoHandler)
	readOnlyApiGroup.GET("/swaps/mnemonic", httpSvc.swapMnemonicHandler)
	readOnlyApiGroup.GET("/autoswap", httpSvc.getAutoSwapConfigHandler)
	readOnlyApiGroup.GET("/forwards", httpSvc.forwardsHandler)
	readOnlyApiGroup.GET("/appstore/apps", httpSvc.getAppStoreAppsHandler)
	readOnlyApiGroup.GET("/lsps2/info", httpSvc.getLSPS2InfoHandler)

	// Full access API group - requires a token with full permissions
	fullAccessApiGroup := e.Group("/api")
	fullAccessApiGroup.Use(echojwt.WithConfig(jwtConfig))
	fullAccessApiGroup.Use(httpSvc.requireFullAccess)

	fullAccessApiGroup.POST("/api/event", httpSvc.eventHandler)
	fullAccessApiGroup.PATCH("/unlock-password", httpSvc.changeUnlockPasswordHandler)
	fullAccessApiGroup.PATCH("/auto-unlock", httpSvc.autoUnlockHandler)
	fullAccessApiGroup.PATCH("/settings", httpSvc.updateSettingsHandler)
	fullAccessApiGroup.PATCH("/apps/:pubkey", httpSvc.appsUpdateHandler)
	fullAccessApiGroup.DELETE("/apps/:pubkey", httpSvc.appsDeleteHandler)
	fullAccessApiGroup.POST("/transfers", httpSvc.transfersHandler)
	fullAccessApiGroup.POST("/apps", httpSvc.appsCreateHandler)

	fullAccessApiGroup.POST("/mnemonic", httpSvc.mnemonicHandler)
	fullAccessApiGroup.PATCH("/backup-reminder", httpSvc.backupReminderHandler)
	fullAccessApiGroup.POST("/channels", httpSvc.openChannelHandler)

	fullAccessApiGroup.POST("/node/migrate-storage", httpSvc.migrateNodeStorageHandler)
	fullAccessApiGroup.POST("/peers", httpSvc.connectPeerHandler)
	fullAccessApiGroup.DELETE("/peers/:peerId", httpSvc.disconnectPeerHandler)
	fullAccessApiGroup.DELETE("/peers/:peerId/channels/:channelId", httpSvc.closeChannelHandler)
	fullAccessApiGroup.PATCH("/peers/:peerId/channels/:channelId", httpSvc.updateChannelHandler)
	fullAccessApiGroup.POST("/wallet/new-address", httpSvc.newOnchainAddressHandler)
	fullAccessApiGroup.POST("/wallet/redeem-onchain-funds", httpSvc.redeemOnchainFundsHandler)
	fullAccessApiGroup.POST("/wallet/sign-message", httpSvc.signMessageHandler)
	fullAccessApiGroup.POST("/wallet/sync", httpSvc.walletSyncHandler)
	fullAccessApiGroup.POST("/payments/:invoice", httpSvc.sendPaymentHandler)
	fullAccessApiGroup.POST("/invoices", httpSvc.makeInvoiceHandler)

	fullAccessApiGroup.POST("/reset-router", httpSvc.resetRouterHandler)
	fullAccessApiGroup.POST("/stop", httpSvc.stopHandler)
	fullAccessApiGroup.POST("/send-payment-probes", httpSvc.sendPaymentProbesHandler)
	fullAccessApiGroup.POST("/send-spontaneous-payment-probes", httpSvc.sendSpontaneousPaymentProbesHandler)
	fullAccessApiGroup.POST("/command", httpSvc.execCustomNodeCommandHandler)
	fullAccessApiGroup.POST("/swaps/out", httpSvc.initiateSwapOutHandler)
	fullAccessApiGroup.POST("/swaps/in", httpSvc.initiateSwapInHandler)
	fullAccessApiGroup.POST("/swaps/refund", httpSvc.refundSwapHandler)
	fullAccessApiGroup.POST("/autoswap", httpSvc.enableAutoSwapOutHandler)
	fullAccessApiGroup.POST("/node/alias", httpSvc.setNodeAliasHandler)
	fullAccessApiGroup.POST("/lsps2/buy", httpSvc.buyLSPS2LiquidityHandler)

	fullAccessApiGroup.GET("/lsps", httpSvc.listLSPsHandler)
	fullAccessApiGroup.POST("/lsps", httpSvc.addLSPHandler)
	fullAccessApiGroup.PUT("/lsps/:pubkey", httpSvc.updateLSPHandler)
	fullAccessApiGroup.DELETE("/lsps/:pubkey", httpSvc.deleteLSPHandler)

	// LSPS0/1/5
	fullAccessApiGroup.GET("/lsps0/protocols", httpSvc.lsps0ListProtocolsHandler)
	fullAccessApiGroup.GET("/lsps1/info", httpSvc.lsps1GetInfoHandler)
	fullAccessApiGroup.POST("/lsps1/order", httpSvc.lsps1CreateOrderHandler)
	fullAccessApiGroup.GET("/lsps1/order", httpSvc.lsps1GetOrderHandler)
	fullAccessApiGroup.GET("/lsps1/orders", httpSvc.lsps1ListOrdersHandler)
	fullAccessApiGroup.GET("/lsps5/webhooks", httpSvc.lsps5ListWebhooksHandler)
	fullAccessApiGroup.POST("/lsps5/webhook", httpSvc.lsps5SetWebhookHandler)
	fullAccessApiGroup.DELETE("/lsps5/webhook", httpSvc.lsps5RemoveWebhookHandler)

	// LSPS5 webhook callback - public endpoint for LSPs to send notifications
	// This must be accessible without auth as external LSPs will call it
	e.POST("/api/lsps5/webhook-callback", httpSvc.lsps5WebhookCallbackHandler)

	// SSE endpoint for LSPS events - requires auth to subscribe
	fullAccessApiGroup.GET("/lsps5/events", httpSvc.lsps5EventsSSEHandler)

	httpSvc.lokiHttpSvc.RegisterSharedRoutes(readOnlyApiGroup, fullAccessApiGroup, e)
}

func (httpSvc *HttpService) infoHandler(c echo.Context) error {
	// Check if user is unlocked
	unlocked := false
	authHeader := c.Request().Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.Split(authHeader, " ")
		if len(parts) == 2 && parts[0] == "Bearer" {
			tokenString := parts[1]
			token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
				secret, err := httpSvc.cfg.GetJWTSecret()
				if err != nil {
					return nil, err
				}
				return []byte(secret), nil
			})
			if err == nil && token != nil && token.Valid {
				unlocked = true
			}
		}
	}

	responseBody, err := httpSvc.api.GetInfo(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	if !unlocked {
		responseBody.WorkDir = "" // Don't expose workdir if not unlocked
	}
	responseBody.Unlocked = unlocked

	return c.JSON(http.StatusOK, responseBody)
}

func (httpSvc *HttpService) eventHandler(c echo.Context) error {
	var sendEventRequest api.SendEventRequest
	if err := c.Bind(&sendEventRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	httpSvc.api.SendEvent(sendEventRequest.Event, sendEventRequest.Properties)

	return c.NoContent(http.StatusOK)
}

func (httpSvc *HttpService) mnemonicHandler(c echo.Context) error {
	var mnemonicRequest api.MnemonicRequest
	if err := c.Bind(&mnemonicRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	responseBody, err := httpSvc.api.GetMnemonic(mnemonicRequest.UnlockPassword)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, responseBody)
}

func (httpSvc *HttpService) backupReminderHandler(c echo.Context) error {
	var backupReminderRequest api.BackupReminderRequest
	if err := c.Bind(&backupReminderRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.SetNextBackupReminder(&backupReminderRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to store backup reminder: %s", err.Error()),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) startHandler(c echo.Context) error {
	var startRequest api.StartRequest
	if err := c.Bind(&startRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	if !httpSvc.cfg.CheckUnlockPassword(startRequest.UnlockPassword) {
		return c.JSON(http.StatusUnauthorized, ErrorResponse{
			Message: "Invalid password",
		})
	}

	err := httpSvc.api.Start(&startRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to start node: %s", err.Error()),
		})
	}

	token, err := httpSvc.createJWT(nil, "full")

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to save session: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, &authTokenResponse{
		Token: token,
	})
}

func (httpSvc *HttpService) unlockHandler(c echo.Context) error {
	var unlockRequest api.UnlockRequest
	if err := c.Bind(&unlockRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	if !httpSvc.cfg.CheckUnlockPassword(unlockRequest.UnlockPassword) {
		return c.JSON(http.StatusUnauthorized, ErrorResponse{
			Message: "Invalid password",
		})
	}

	if unlockRequest.Permission == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: "Permission field is required",
		})
	}

	if !slices.Contains([]string{"full", "readonly"}, unlockRequest.Permission) {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: "Permission field is unknown",
		})
	}

	token, err := httpSvc.createJWT(unlockRequest.TokenExpiryDays, unlockRequest.Permission)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to save session: %s", err.Error()),
		})
	}

	httpSvc.eventPublisher.Publish(&events.Event{
		Event: "nwc_unlocked",
	})

	return c.JSON(http.StatusOK, &authTokenResponse{
		Token: token,
	})
}

func (httpSvc *HttpService) requireFullAccess(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		token := c.Get("user").(*jwt.Token)
		claims := token.Claims.(*jwtCustomClaims)

		// Allow if no permission specified (backward compatibility) or if full access
		if claims.Permission == "" || claims.Permission == "full" {
			return next(c)
		}

		return c.JSON(http.StatusForbidden, ErrorResponse{
			Message: "This operation requires full access permissions",
		})
	}
}

func (httpSvc *HttpService) changeUnlockPasswordHandler(c echo.Context) error {
	var changeUnlockPasswordRequest api.ChangeUnlockPasswordRequest
	if err := c.Bind(&changeUnlockPasswordRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.ChangeUnlockPassword(&changeUnlockPasswordRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to change unlock password: %s", err.Error()),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) updateSettingsHandler(c echo.Context) error {
	var updateSettingsRequest api.UpdateSettingsRequest
	if err := c.Bind(&updateSettingsRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.UpdateSettings(&updateSettingsRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to update settings: %s", err.Error()),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) autoUnlockHandler(c echo.Context) error {
	var autoUnlockRequest api.AutoUnlockRequest
	if err := c.Bind(&autoUnlockRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.SetAutoUnlockPassword(autoUnlockRequest.UnlockPassword)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to set auto unlock password: %s", err.Error()),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) createJWT(tokenExpiryDays *uint64, permission string) (string, error) {
	if !slices.Contains([]string{"full", "readonly"}, permission) {
		return "", errors.New("invalid token permission")
	}

	expiryDays := uint64(30)
	if tokenExpiryDays != nil {
		expiryDays = *tokenExpiryDays
	}

	// Set custom claims
	claims := &jwtCustomClaims{
		Permission: permission,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour * 24 * time.Duration(expiryDays))),
		},
	}

	// Create token with claims
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	if token == nil {
		return "", errors.New("failed to create token")
	}

	secret, err := httpSvc.cfg.GetJWTSecret()
	if err != nil {
		return "", err
	}

	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", err
	}
	return signed, nil
}

func (httpSvc *HttpService) channelsListHandler(c echo.Context) error {
	ctx := c.Request().Context()

	channels, err := httpSvc.api.ListChannels(ctx)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, channels)
}

func (httpSvc *HttpService) resetRouterHandler(c echo.Context) error {
	var resetRouterRequest api.ResetRouterRequest
	if err := c.Bind(&resetRouterRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.ResetRouter(resetRouterRequest.Key)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) stopHandler(c echo.Context) error {

	err := httpSvc.api.Stop()

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) nodeConnectionInfoHandler(c echo.Context) error {
	ctx := c.Request().Context()

	info, err := httpSvc.api.GetNodeConnectionInfo(ctx)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, info)
}

func (httpSvc *HttpService) nodeStatusHandler(c echo.Context) error {
	ctx := c.Request().Context()

	info, err := httpSvc.api.GetNodeStatus(ctx)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, info)
}

func (httpSvc *HttpService) nodeNetworkGraphHandler(c echo.Context) error {
	ctx := c.Request().Context()

	nodeIds := strings.Split(c.QueryParam("nodeIds"), ",")

	info, err := httpSvc.api.GetNetworkGraph(ctx, nodeIds)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, info)
}

func (httpSvc *HttpService) migrateNodeStorageHandler(c echo.Context) error {
	ctx := c.Request().Context()
	var migrateNodeStorageRequest api.MigrateNodeStorageRequest
	if err := c.Bind(&migrateNodeStorageRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.MigrateNodeStorage(ctx, migrateNodeStorageRequest.To)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) balancesHandler(c echo.Context) error {
	ctx := c.Request().Context()

	balances, err := httpSvc.api.GetBalances(ctx)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, balances)
}

func (httpSvc *HttpService) sendPaymentHandler(c echo.Context) error {
	ctx := c.Request().Context()

	var payInvoiceRequest api.PayInvoiceRequest
	if err := c.Bind(&payInvoiceRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	paymentResponse, err := httpSvc.api.SendPayment(ctx, c.Param("invoice"), payInvoiceRequest.Amount, payInvoiceRequest.Metadata)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, paymentResponse)
}

func (httpSvc *HttpService) makeInvoiceHandler(c echo.Context) error {
	var makeInvoiceRequest api.MakeInvoiceRequest
	if err := c.Bind(&makeInvoiceRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	invoice, err := httpSvc.api.CreateInvoice(c.Request().Context(), &makeInvoiceRequest)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, invoice)
}

func (httpSvc *HttpService) lookupTransactionHandler(c echo.Context) error {
	ctx := c.Request().Context()

	transaction, err := httpSvc.api.LookupInvoice(ctx, c.Param("paymentHash"))

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, transaction)
}

func (httpSvc *HttpService) listTransactionsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	limit := uint64(20)
	offset := uint64(0)
	var appId *uint

	if limitParam := c.QueryParam("limit"); limitParam != "" {
		if parsedLimit, err := strconv.ParseUint(limitParam, 10, 64); err == nil {
			limit = parsedLimit
		}
	}

	if offsetParam := c.QueryParam("offset"); offsetParam != "" {
		if parsedOffset, err := strconv.ParseUint(offsetParam, 10, 64); err == nil {
			offset = parsedOffset
		}
	}

	if appIdParam := c.QueryParam("appId"); appIdParam != "" {
		if parsedAppId, err := strconv.ParseUint(appIdParam, 10, 64); err == nil {
			var unsignedAppId = uint(parsedAppId)
			appId = &unsignedAppId
		}
	}

	transactions, err := httpSvc.api.ListTransactions(ctx, appId, limit, offset)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, transactions)
}

func (httpSvc *HttpService) listOnchainTransactionsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	limit := uint64(0)
	offset := uint64(0)

	if limitParam := c.QueryParam("limit"); limitParam != "" {
		if parsedLimit, err := strconv.ParseUint(limitParam, 10, 64); err == nil {
			limit = parsedLimit
		}
	}

	if offsetParam := c.QueryParam("offset"); offsetParam != "" {
		if parsedOffset, err := strconv.ParseUint(offsetParam, 10, 64); err == nil {
			offset = parsedOffset
		}
	}

	transactions, err := httpSvc.api.ListOnchainTransactions(ctx, limit, offset)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, transactions)
}

func (httpSvc *HttpService) walletSyncHandler(c echo.Context) error {
	httpSvc.api.SyncWallet()

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) mempoolApiHandler(c echo.Context) error {
	endpoint := c.QueryParam("endpoint")
	if endpoint == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: "Invalid pubkey parameter",
		})
	}

	response, err := httpSvc.api.RequestMempoolApi(c.Request().Context(), endpoint)
	if err != nil {
		logger.Logger.Error().Err(err).Str("endpoint", endpoint).Msg("Failed to request mempool API")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to request mempool API: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, response)
}

func (httpSvc *HttpService) getServicesHandler(c echo.Context) error {
	response, err := httpSvc.api.GetServices(c.Request().Context())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch services")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to fetch services: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, response)
}

func (httpSvc *HttpService) capabilitiesHandler(c echo.Context) error {
	response, err := httpSvc.api.GetWalletCapabilities(c.Request().Context())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to request wallet capabilities")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to request wallet capabilities: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, response)
}

func (httpSvc *HttpService) listPeers(c echo.Context) error {
	peers, err := httpSvc.api.ListPeers(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to list peers: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, peers)
}

func (httpSvc *HttpService) connectPeerHandler(c echo.Context) error {
	ctx := c.Request().Context()

	var connectPeerRequest api.ConnectPeerRequest
	if err := c.Bind(&connectPeerRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.ConnectPeer(ctx, &connectPeerRequest)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to connect peer: %s", err.Error()),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) openChannelHandler(c echo.Context) error {
	ctx := c.Request().Context()

	var openChannelRequest api.OpenChannelRequest
	if err := c.Bind(&openChannelRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	openChannelResponse, err := httpSvc.api.OpenChannel(ctx, &openChannelRequest)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to open channel: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, openChannelResponse)
}

func (httpSvc *HttpService) disconnectPeerHandler(c echo.Context) error {
	ctx := c.Request().Context()

	err := httpSvc.api.DisconnectPeer(ctx, c.Param("peerId"))

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to disconnect peer: %s", err.Error()),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) closeChannelHandler(c echo.Context) error {
	ctx := c.Request().Context()

	closeChannelResponse, err := httpSvc.api.CloseChannel(ctx, c.Param("peerId"), c.Param("channelId"), c.QueryParam("force") == "true")

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to close channel: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, closeChannelResponse)
}

func (httpSvc *HttpService) updateChannelHandler(c echo.Context) error {
	ctx := c.Request().Context()

	var updateChannelRequest api.UpdateChannelRequest
	if err := c.Bind(&updateChannelRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	updateChannelRequest.NodeId = c.Param("peerId")
	updateChannelRequest.ChannelId = c.Param("channelId")

	err := httpSvc.api.UpdateChannel(ctx, &updateChannelRequest)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to update channel: %s", err.Error()),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) onchainAddressHandler(c echo.Context) error {
	ctx := c.Request().Context()

	address, err := httpSvc.api.GetUnusedOnchainAddress(ctx)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to request new onchain address: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, address)
}

func (httpSvc *HttpService) newOnchainAddressHandler(c echo.Context) error {
	ctx := c.Request().Context()

	address, err := httpSvc.api.GetNewOnchainAddress(ctx)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to request new onchain address: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, address)
}

func (httpSvc *HttpService) redeemOnchainFundsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	var redeemOnchainFundsRequest api.RedeemOnchainFundsRequest
	if err := c.Bind(&redeemOnchainFundsRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	redeemOnchainFundsResponse, err := httpSvc.api.RedeemOnchainFunds(ctx, redeemOnchainFundsRequest.ToAddress, redeemOnchainFundsRequest.Amount, redeemOnchainFundsRequest.FeeRate, redeemOnchainFundsRequest.SendAll)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to redeem onchain funds: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, redeemOnchainFundsResponse)
}

func (httpSvc *HttpService) signMessageHandler(c echo.Context) error {
	ctx := c.Request().Context()

	var signMessageRequest api.SignMessageRequest
	if err := c.Bind(&signMessageRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	signMessageResponse, err := httpSvc.api.SignMessage(ctx, signMessageRequest.Message)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to sign message: %s", err.Error()),
		})
	}
	return c.JSON(http.StatusOK, signMessageResponse)
}

func (httpSvc *HttpService) appsListHandler(c echo.Context) error {
	limit := uint64(0)
	offset := uint64(0)

	if limitParam := c.QueryParam("limit"); limitParam != "" {
		if parsedLimit, err := strconv.ParseUint(limitParam, 10, 64); err == nil {
			limit = parsedLimit
		}
	}

	if offsetParam := c.QueryParam("offset"); offsetParam != "" {
		if parsedOffset, err := strconv.ParseUint(offsetParam, 10, 64); err == nil {
			offset = parsedOffset
		}
	}

	filtersJSON := c.QueryParam("filters")
	var filters api.ListAppsFilters
	if filtersJSON != "" {
		err := json.Unmarshal([]byte(filtersJSON), &filters)
		if err != nil {
			logger.Logger.Error().Err(err).
				Str("filters", filtersJSON).
				Msg("Failed to deserialize app filters")
			return err
		}
	}

	orderBy := c.QueryParam("order_by")

	apps, err := httpSvc.api.ListApps(limit, offset, filters, orderBy)

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, apps)
}

func (httpSvc *HttpService) appsShowByPubkeyHandler(c echo.Context) error {
	dbApp := httpSvc.appsSvc.GetAppByPubkey(c.Param("pubkey"))

	if dbApp == nil {
		return c.JSON(http.StatusNotFound, ErrorResponse{
			Message: "App not found",
		})
	}

	response := httpSvc.api.GetApp(dbApp)

	return c.JSON(http.StatusOK, response)
}

func (httpSvc *HttpService) appsShowHandler(c echo.Context) error {
	appIdStr := c.Param("id")
	if appIdStr == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: "App ID is required",
		})
	}

	appId, err := strconv.ParseUint(appIdStr, 10, 64)
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: "Invalid App ID",
		})
	}

	dbApp := httpSvc.appsSvc.GetAppById(uint(appId))

	if dbApp == nil {
		return c.JSON(http.StatusNotFound, ErrorResponse{
			Message: "App not found",
		})
	}

	response := httpSvc.api.GetApp(dbApp)

	return c.JSON(http.StatusOK, response)
}

func (httpSvc *HttpService) appsUpdateHandler(c echo.Context) error {
	var requestData api.UpdateAppRequest
	if err := c.Bind(&requestData); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	param := c.Param("pubkey")
	var dbApp *lokidb.App
	if id, err := strconv.ParseUint(param, 10, 64); err == nil {
		dbApp = httpSvc.appsSvc.GetAppById(uint(id))
	} else {
		dbApp = httpSvc.appsSvc.GetAppByPubkey(param)
	}

	if dbApp == nil {
		return c.JSON(http.StatusNotFound, ErrorResponse{
			Message: "App not found",
		})
	}

	err := httpSvc.api.UpdateApp(dbApp, &requestData)

	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to update app")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to update app: %v", err),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) transfersHandler(c echo.Context) error {
	var requestData api.TransferRequest
	if err := c.Bind(&requestData); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.Transfer(c.Request().Context(), requestData.FromAppId, requestData.ToAppId, requestData.AmountLoki*1000)

	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to transfer funds")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to transfer funds: %v", err),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) appsDeleteHandler(c echo.Context) error {
	param := c.Param("pubkey")
	var dbApp *lokidb.App
	if id, err := strconv.ParseUint(param, 10, 64); err == nil {
		dbApp = httpSvc.appsSvc.GetAppById(uint(id))
	} else {
		dbApp = httpSvc.appsSvc.GetAppByPubkey(param)
	}
	if dbApp == nil {
		return c.JSON(http.StatusNotFound, ErrorResponse{
			Message: "App not found",
		})
	}

	if err := httpSvc.api.DeleteApp(dbApp); err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: "Failed to delete app",
		})
	}
	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) appsCreateHandler(c echo.Context) error {
	var requestData api.CreateAppRequest
	if err := c.Bind(&requestData); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	responseBody, err := httpSvc.api.CreateApp(&requestData)

	if err != nil {
		logger.Logger.Error().Err(err).Interface("requestData", requestData).Msg("Failed to save app")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to save app: %v", err),
		})
	}

	return c.JSON(http.StatusOK, responseBody)
}

func (httpSvc *HttpService) setupHandler(c echo.Context) error {
	var setupRequest api.SetupRequest
	if err := c.Bind(&setupRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.Setup(c.Request().Context(), &setupRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to setup node: %s", err.Error()),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) setupStatusHandler(c echo.Context) error {
	status, err := httpSvc.api.GetSetupStatus(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to get setup status: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, status)
}

func (httpSvc *HttpService) sendPaymentProbesHandler(c echo.Context) error {
	var sendPaymentProbesRequest api.SendPaymentProbesRequest
	if err := c.Bind(&sendPaymentProbesRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	sendPaymentProbesResponse, err := httpSvc.api.SendPaymentProbes(c.Request().Context(), &sendPaymentProbesRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to send payment probes: %v", err),
		})
	}

	return c.JSON(http.StatusOK, sendPaymentProbesResponse)
}

func (httpSvc *HttpService) sendSpontaneousPaymentProbesHandler(c echo.Context) error {
	var sendSpontaneousPaymentProbesRequest api.SendSpontaneousPaymentProbesRequest
	if err := c.Bind(&sendSpontaneousPaymentProbesRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	sendSpontaneousPaymentProbesResponse, err := httpSvc.api.SendSpontaneousPaymentProbes(c.Request().Context(), &sendSpontaneousPaymentProbesRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to send spontaneous payment probes: %v", err),
		})
	}

	return c.JSON(http.StatusOK, sendSpontaneousPaymentProbesResponse)
}

func (httpSvc *HttpService) getLogOutputHandler(c echo.Context) error {
	var getLogRequest api.GetLogOutputRequest
	if err := c.Bind(&getLogRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	logType := c.Param("type")
	if logType != api.LogTypeNode && logType != api.LogTypeApp {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Invalid log type parameter: '%s'", logType),
		})
	}

	getLogResponse, err := httpSvc.api.GetLogOutput(c.Request().Context(), logType, &getLogRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to get log output: %v", err),
		})
	}

	return c.JSON(http.StatusOK, getLogResponse)
}

func (httpSvc *HttpService) getCustomNodeCommandsHandler(c echo.Context) error {
	nodeCommandsResponse, err := httpSvc.api.GetCustomNodeCommands()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to get node commands: %v", err),
		})
	}

	return c.JSON(http.StatusOK, nodeCommandsResponse)
}

func (httpSvc *HttpService) execCustomNodeCommandHandler(c echo.Context) error {
	var execCommandRequest api.ExecuteCustomNodeCommandRequest
	if err := c.Bind(&execCommandRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	execCommandResponse, err := httpSvc.api.ExecuteCustomNodeCommand(c.Request().Context(), execCommandRequest.Command)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to execute command: %v", err),
		})
	}

	return c.JSON(http.StatusOK, execCommandResponse)
}

func (httpSvc *HttpService) logoutHandler(c echo.Context) error {
	redirectUrl := httpSvc.cfg.GetEnv().GetBaseFrontendUrl()
	if redirectUrl == "" {
		redirectUrl = "/"
	}

	return c.Redirect(http.StatusFound, redirectUrl)
}

func (httpSvc *HttpService) createBackupHandler(c echo.Context) error {
	var backupRequest api.BasicBackupRequest
	if err := c.Bind(&backupRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	if !httpSvc.cfg.CheckUnlockPassword(backupRequest.UnlockPassword) {
		return c.JSON(http.StatusUnauthorized, ErrorResponse{
			Message: "Invalid password",
		})
	}

	var buffer bytes.Buffer
	err := httpSvc.api.CreateBackup(backupRequest.UnlockPassword, &buffer)
	if err != nil {
		return c.String(500, fmt.Sprintf("Failed to create backup: %v", err))
	}

	c.Response().Header().Set("Content-Type", "application/octet-stream")
	c.Response().Header().Set("Content-Disposition", "attachment; filename=lokihub.bkp")
	c.Response().WriteHeader(http.StatusOK)
	c.Response().Write(buffer.Bytes())
	return nil
}

func (httpSvc *HttpService) restoreBackupHandler(c echo.Context) error {
	info, err := httpSvc.api.GetInfo(c.Request().Context())
	if err != nil {
		return err
	}
	if info.SetupCompleted {
		return errors.New("setup already completed")
	}

	password := c.FormValue("unlockPassword")

	fileHeader, err := c.FormFile("backup")
	if err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Failed to get backup file header: %v", err),
		})
	}

	file, err := fileHeader.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to open backup file: %v", err),
		})
	}
	defer file.Close()

	err = httpSvc.api.RestoreBackup(password, file)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to restore backup: %v", err),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) healthHandler(c echo.Context) error {
	healthResponse, err := httpSvc.api.Health(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to check node health: %v", err),
		})
	}

	return c.JSON(http.StatusOK, healthResponse)
}

func (httpSvc *HttpService) listSwapsHandler(c echo.Context) error {
	swaps, err := httpSvc.api.ListSwaps()

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, swaps)
}

func (httpSvc *HttpService) lookupSwapHandler(c echo.Context) error {
	swap, err := httpSvc.api.LookupSwap(c.Param("swapId"))
	if err != nil {
		return c.JSON(http.StatusNotFound, ErrorResponse{
			Message: "App not found",
		})
	}

	return c.JSON(http.StatusOK, swap)
}

func (httpSvc *HttpService) getSwapOutInfoHandler(c echo.Context) error {
	swapOutFeesResponse, err := httpSvc.api.GetSwapOutInfo()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to get swap out info: %v", err),
		})
	}

	return c.JSON(http.StatusOK, swapOutFeesResponse)
}

func (httpSvc *HttpService) getSwapInInfoHandler(c echo.Context) error {
	swapOutFeesResponse, err := httpSvc.api.GetSwapInInfo()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to get swap in info: %v", err),
		})
	}

	return c.JSON(http.StatusOK, swapOutFeesResponse)
}

func (httpSvc *HttpService) initiateSwapOutHandler(c echo.Context) error {
	var initiateSwapOutRequest api.InitiateSwapRequest
	if err := c.Bind(&initiateSwapOutRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	swapOutResponse, err := httpSvc.api.InitiateSwapOut(c.Request().Context(), &initiateSwapOutRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to initiate swap out: %v", err),
		})
	}

	return c.JSON(http.StatusOK, swapOutResponse)
}

func (httpSvc *HttpService) initiateSwapInHandler(c echo.Context) error {
	var initiateSwapInRequest api.InitiateSwapRequest
	if err := c.Bind(&initiateSwapInRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	txId, err := httpSvc.api.InitiateSwapIn(c.Request().Context(), &initiateSwapInRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to initiate swap in: %v", err),
		})
	}

	return c.JSON(http.StatusOK, txId)
}

func (httpSvc *HttpService) refundSwapHandler(c echo.Context) error {
	var refundSwapInRequest api.RefundSwapRequest
	if err := c.Bind(&refundSwapInRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.RefundSwap(&refundSwapInRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) swapMnemonicHandler(c echo.Context) error {
	mnemonic := httpSvc.api.GetSwapMnemonic()
	return c.JSON(http.StatusOK, mnemonic)
}

func (httpSvc *HttpService) getAutoSwapConfigHandler(c echo.Context) error {
	getAutoSwapConfigResponse, err := httpSvc.api.GetAutoSwapConfig()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to get swap settings: %v", err),
		})
	}

	return c.JSON(http.StatusOK, getAutoSwapConfigResponse)
}

func (httpSvc *HttpService) enableAutoSwapOutHandler(c echo.Context) error {
	var enableAutoSwapRequest api.EnableAutoSwapRequest
	if err := c.Bind(&enableAutoSwapRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.EnableAutoSwapOut(c.Request().Context(), &enableAutoSwapRequest)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to save swap settings: %v", err),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) disableAutoSwapOutHandler(c echo.Context) error {
	err := httpSvc.api.DisableAutoSwap()

	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: err.Error(),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) setNodeAliasHandler(c echo.Context) error {
	var setNodeAliasRequest api.SetNodeAliasRequest
	if err := c.Bind(&setNodeAliasRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.SetNodeAlias(c.Request().Context(), setNodeAliasRequest.NodeAlias)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to set node alias: %s", err.Error()),
		})
	}

	return c.NoContent(http.StatusNoContent)
}

func (httpSvc *HttpService) forwardsHandler(c echo.Context) error {
	forwards, err := httpSvc.api.GetForwards()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to get forwards: %s", err.Error()),
		})
	}

	return c.JSON(http.StatusOK, forwards)
}

func (httpSvc *HttpService) setupLocalHandler(c echo.Context) error {
	var setupRequest api.SetupLocalRequest
	if err := c.Bind(&setupRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.SetupLocal(c.Request().Context(), &setupRequest)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to setup local connection")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to setup: %s", err.Error()),
		})
	}

	return c.NoContent(http.StatusOK)
}

func (httpSvc *HttpService) setupManualHandler(c echo.Context) error {
	var setupRequest api.SetupManualRequest
	if err := c.Bind(&setupRequest); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	err := httpSvc.api.SetupManual(c.Request().Context(), &setupRequest)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to setup manual connection")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Message: fmt.Sprintf("Failed to setup: %s", err.Error()),
		})
	}

	return c.NoContent(http.StatusOK)
}

func (httpSvc *HttpService) getAppStoreAppsHandler(c echo.Context) error {
	apps := httpSvc.appStoreSvc.ListApps()
	return c.JSON(http.StatusOK, apps)
}

func (httpSvc *HttpService) getAppStoreLogoHandler(c echo.Context) error {
	appId := c.Param("appId")
	logger.Logger.Info().Str("appId", appId).Msg("getAppStoreLogoHandler called")
	if appId == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "appId is required"})
	}

	logoPath, err := httpSvc.appStoreSvc.GetLogoPath(appId)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("GetLogoPath failed")
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Message: err.Error()})
	}
	logger.Logger.Info().Str("path", logoPath).Msg("Resolved logo path")

	// Manual file serving to debug issues
	f, err := os.Open(logoPath)
	if err != nil {
		logger.Logger.Error().Err(err).Str("path", logoPath).Msg("Failed to open logo file")
		if os.IsNotExist(err) {
			return c.JSON(http.StatusNotFound, ErrorResponse{Message: "Logo not found"})
		}
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Message: fmt.Sprintf("Failed to open file: %s", err.Error())})
	}
	defer f.Close()

	// Detect content type or assume png
	return c.Stream(http.StatusOK, "image/png", f)
}

func (httpSvc *HttpService) getLSPS2InfoHandler(c echo.Context) error {
	lspPubkey := c.QueryParam("lsp")
	if lspPubkey == "" {
		lspPubkey = c.QueryParam("lspPubkey")
	}
	if lspPubkey == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: "lsp (or lspPubkey) is required",
		})
	}

	req := &api.LSPS2GetInfoRequest{
		LSPPubkey: lspPubkey,
	}

	response, err := httpSvc.api.LSPS2GetInfo(c.Request().Context(), req)
	if err != nil {
		return handleErrorWithTimeout(c, err)
	}

	return c.JSON(http.StatusOK, response)
}

func (httpSvc *HttpService) buyLSPS2LiquidityHandler(c echo.Context) error {
	var req api.LSPS2BuyRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Message: fmt.Sprintf("Bad request: %s", err.Error()),
		})
	}

	response, err := httpSvc.api.LSPS2Buy(c.Request().Context(), &req)
	if err != nil {
		return handleErrorWithTimeout(c, err)
	}

	return c.JSON(http.StatusOK, response)
}

// Helper to check for timeouts
func handleErrorWithTimeout(c echo.Context, err error) error {
	if strings.Contains(strings.ToLower(err.Error()), "time out") || errors.Is(err, context.DeadlineExceeded) {
		return c.JSON(http.StatusGatewayTimeout, ErrorResponse{Message: "Request timed out, please retry"})
	}
	return c.JSON(http.StatusInternalServerError, ErrorResponse{Message: err.Error()})
}

func (httpSvc *HttpService) lsps0ListProtocolsHandler(c echo.Context) error {
	lspPubkey := c.QueryParam("lsp")
	if lspPubkey == "" {
		lspPubkey = c.QueryParam("lspPubkey")
	}
	if lspPubkey == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "lsp (or lspPubkey) is required"})
	}
	resp, err := httpSvc.api.LSPS0ListProtocols(c.Request().Context(), &api.LSPS0ListProtocolsRequest{LSPPubkey: lspPubkey})
	if err != nil {
		return handleErrorWithTimeout(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (httpSvc *HttpService) lsps1GetInfoHandler(c echo.Context) error {
	lspPubkey := c.QueryParam("lsp")
	token := c.QueryParam("token")
	if lspPubkey == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "lsp is required"})
	}
	resp, err := httpSvc.api.LSPS1GetInfo(c.Request().Context(), &api.LSPS1GetInfoRequest{LSPPubkey: lspPubkey, Token: token})
	if err != nil {
		return handleErrorWithTimeout(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (httpSvc *HttpService) lsps1CreateOrderHandler(c echo.Context) error {
	var req api.LSPS1CreateOrderRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
	}
	resp, err := httpSvc.api.LSPS1CreateOrder(c.Request().Context(), &req)
	if err != nil {
		return handleErrorWithTimeout(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (httpSvc *HttpService) lsps1GetOrderHandler(c echo.Context) error {
	lspPubkey := c.QueryParam("lsp")
	if lspPubkey == "" {
		lspPubkey = c.QueryParam("lspPubkey")
	}
	orderId := c.QueryParam("orderId")
	token := c.QueryParam("token")
	if lspPubkey == "" || orderId == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "lsp (or lspPubkey) and orderId are required"})
	}
	resp, err := httpSvc.api.LSPS1GetOrder(c.Request().Context(), &api.LSPS1GetOrderRequest{LSPPubkey: lspPubkey, OrderID: orderId, Token: token})
	if err != nil {
		return handleErrorWithTimeout(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (httpSvc *HttpService) lsps1ListOrdersHandler(c echo.Context) error {
	resp, err := httpSvc.api.LSPS1ListOrders(c.Request().Context())
	if err != nil {
		return handleErrorWithTimeout(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (httpSvc *HttpService) lsps5ListWebhooksHandler(c echo.Context) error {
	lspPubkey := c.QueryParam("lsp")
	if lspPubkey == "" {
		lspPubkey = c.QueryParam("lspPubkey")
	}
	if lspPubkey == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "lsp (or lspPubkey) is required"})
	}
	resp, err := httpSvc.api.LSPS5ListWebhooks(c.Request().Context(), &api.LSPS5ListWebhooksRequest{LSPPubkey: lspPubkey})
	if err != nil {
		return handleErrorWithTimeout(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (httpSvc *HttpService) lsps5SetWebhookHandler(c echo.Context) error {
	var req api.LSPS5SetWebhookRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
	}
	resp, err := httpSvc.api.LSPS5SetWebhook(c.Request().Context(), &req)
	if err != nil {
		return handleErrorWithTimeout(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (httpSvc *HttpService) lsps5RemoveWebhookHandler(c echo.Context) error {
	var req api.LSPS5RemoveWebhookRequest
	// Try bind first (POST/DELETE with body), fallback to query params
	if err := c.Bind(&req); err != nil || req.LSPPubkey == "" || req.URL == "" {
		req.LSPPubkey = c.QueryParam("lsp")
		if req.LSPPubkey == "" {
			req.LSPPubkey = c.QueryParam("lspPubkey")
		}
		req.URL = c.QueryParam("url")
	}

	if req.LSPPubkey == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "lsp (or lspPubkey) is required"})
	}

	resp, err := httpSvc.api.LSPS5RemoveWebhook(c.Request().Context(), &req)
	if err != nil {
		return handleErrorWithTimeout(c, err)
	}
	return c.JSON(http.StatusOK, resp)
}

func (httpSvc *HttpService) estimateInvoiceFeeHandler(c echo.Context) error {
	type EstimateFeeRequest struct {
		Invoice string `json:"invoice"`
	}
	req := &EstimateFeeRequest{}
	if err := c.Bind(req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: err.Error()})
	}
	if req.Invoice == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Message: "invoice is required"})
	}

	fee, err := httpSvc.api.EstimateInvoiceFee(c.Request().Context(), req.Invoice)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Message: err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]uint64{"estimatedFeeMloki": fee})
}
