package main

import (
	"context"
	"fmt"
	nethttp "net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/http"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/service"
	"github.com/labstack/echo/v4"
)

func main() {
	logger.Logger.Info().Msg("Lokihub Starting in HTTP mode")

	// Create a channel to receive OS signals.
	osSignalChannel := make(chan os.Signal, 1)
	// Notify the channel on os.Interrupt, syscall.SIGTERM. os.Kill cannot be caught.
	signal.Notify(osSignalChannel, os.Interrupt, syscall.SIGTERM, syscall.SIGPIPE)

	ctx, cancel := context.WithCancel(context.Background())

	var signal os.Signal
	go func() {
		for {
			// wait for exit signal
			signal = <-osSignalChannel
			logger.Logger.Info().Str("signal", signal.String()).Msg("Received OS signal")

			if signal == syscall.SIGPIPE {
				logger.Logger.Warn().Str("signal", signal.String()).Msg("Ignoring SIGPIPE signal")
				continue
			}

			cancel()
			break
		}
	}()

	svc, err := service.NewService(ctx)
	if err != nil {
		logger.Logger.Fatal().Err(err).Msg("Failed to create service")
		return
	}

	e := echo.New()

	//register shared routes
	httpSvc := http.NewHttpService(svc, svc.GetEventPublisher())
	httpSvc.RegisterSharedRoutes(e)
	//start Echo server
	go func() {
		if err := e.Start(fmt.Sprintf(":%v", svc.GetConfig().GetEnv().Port)); err != nil && err != nethttp.ErrServerClosed {
			logger.Logger.Error().Err(err).Msg("echo server failed to start")
			cancel()
		}
	}()

	//handle graceful shutdown
	<-ctx.Done()
	if signal != nil {
		logger.Logger.Info().Str("signal", signal.String()).Msg("Shutting down")
	} else {
		logger.Logger.Info().Msg("Shutting down (triggered internally)")
	}
	// unblock long-lived handlers (e.g. SSE streams) so they don't stall
	// echo's Shutdown() below, which only waits for connections to go idle
	// and never cancels in-flight request contexts on its own.
	httpSvc.Shutdown()

	echoShutdownCtx, echoShutdownCancel := context.WithTimeout(context.Background(), constants.APP_SHUTDOWN_TIMEOUT)
	defer echoShutdownCancel()
	err = e.Shutdown(echoShutdownCtx)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to shutdown echo server")
	}
	logger.Logger.Info().Msg("Echo server exited")

	// use a fresh timeout budget so a slow/stuck echo shutdown above cannot
	// starve the app subsystems below of their own shutdown window
	svcShutdownCtx, svcShutdownCancel := context.WithTimeout(context.Background(), constants.APP_SHUTDOWN_TIMEOUT)
	defer svcShutdownCancel()
	svc.Shutdown(svcShutdownCtx)
	logger.Logger.Info().Msg("Service exited")
	logger.Logger.Info().Msg("Lokihub needs to stay online to send and receive transactions. ")
}
