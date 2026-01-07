//go:build wails
// +build wails

package main

import (
	"context"
	"embed"
	"net"

	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/service"
	"github.com/flokiorg/lokihub/wails"
	log "github.com/sirupsen/logrus"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed appicon.png
var appIcon []byte

func main() {
	// Get a port lock on a rare port to prevent the app running twice
	listener, err := net.Listen("tcp", "0.0.0.0:25521")
	if err != nil {
		log.Println("Another instance of Lokihub is already running.")
		return
	}
	defer listener.Close()

	log.Info("Lokihub starting in WAILS mode")
	ctx, cancel := context.WithCancel(context.Background())
	svc, err := service.NewService(ctx)
	if err != nil {
		log.WithError(err).Fatal("Failed to create service")
		return
	}

	app := wails.NewApp(svc)
	wails.LaunchWailsApp(app, assets, appIcon)
	logger.Logger.Info("Wails app exited")

	logger.Logger.Info("Cancelling service context...")
	// cancel the service context
	cancel()
	svc.Shutdown()
	logger.Logger.Info("Service exited")
	logger.Logger.Info("Lokihub needs to stay online to send and receive transactions. Channels may be closed if your hub stays offline for an extended period of time.")
}
