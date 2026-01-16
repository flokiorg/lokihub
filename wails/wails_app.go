package wails

import (
	"context"
	"embed"

	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/service"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"gorm.io/gorm"
)

type WailsApp struct {
	ctx     context.Context
	svc     service.Service
	api     api.API
	db      *gorm.DB
	appsSvc apps.AppsService
}

func NewApp(svc service.Service) *WailsApp {
	return &WailsApp{
		svc:     svc,
		api:     api.NewAPI(svc, svc.GetDB(), svc.GetConfig(), svc.GetKeys(), svc.GetLokiSvc(), svc.GetEventPublisher()),
		db:      svc.GetDB(),
		appsSvc: apps.NewAppsService(svc.GetDB(), svc.GetEventPublisher(), svc.GetKeys(), svc.GetConfig()),
	}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (app *WailsApp) startup(ctx context.Context) {
	app.ctx = ctx
}

func (app *WailsApp) onBeforeClose(ctx context.Context) bool {
	response, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
		Type:  runtime.QuestionDialog,
		Title: "Confirm Exit",
		Message: "Are you sure you want to shut down Lokihub? " +
			"Lokihub must remain online to coordinate payments." +
			"If it is offline, creating or sending payments may fail.",
		Buttons:       []string{"Yes", "No"},
		DefaultButton: "No",
	})
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to show confirmation dialog")
		return false
	}
	return response != "Yes"
}

func (app *WailsApp) SelectDirectory() (string, error) {
	selection, err := runtime.OpenDirectoryDialog(app.ctx, runtime.OpenDialogOptions{
		Title: "Select Work Directory",
	})
	if err != nil {
		return "", err
	}
	return selection, nil
}

func LaunchWailsApp(app *WailsApp, assets embed.FS, appIcon []byte) {
	err := wails.Run(&options.App{
		Title:  "Lokihub",
		Width:  1055,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		Logger: NewWailsLogger(),
		// HideWindowOnClose: true, // with this on, there is no way to close the app - wait for v3

		OnStartup: app.startup,
		OnBeforeClose: func(ctx context.Context) bool {
			return app.onBeforeClose(ctx)
		},
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			About: &mac.AboutInfo{
				Title: "Lokihub",
				Icon:  appIcon,
			},
		},
		Linux: &linux.Options{
			Icon: appIcon,
		},
	})

	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to run Wails app")
	}
}

func NewWailsLogger() WailsLogger {
	return WailsLogger{}
}

type WailsLogger struct {
}

func (wailsLogger WailsLogger) Print(message string) {
	logger.Logger.Info().Bool("wails", true).Msg(message)
}

func (wailsLogger WailsLogger) Trace(message string) {
	logger.Logger.Trace().Bool("wails", true).Msg(message)
}

func (wailsLogger WailsLogger) Debug(message string) {
	logger.Logger.Debug().Bool("wails", true).Msg(message)
}

func (wailsLogger WailsLogger) Info(message string) {
	logger.Logger.Info().Bool("wails", true).Msg(message)
}

func (wailsLogger WailsLogger) Warning(message string) {
	logger.Logger.Warn().Bool("wails", true).Msg(message)
}

func (wailsLogger WailsLogger) Error(message string) {
	logger.Logger.Error().Bool("wails", true).Msg(message)
}

func (wailsLogger WailsLogger) Fatal(message string) {
	logger.Logger.Fatal().Bool("wails", true).Msg(message)
}
