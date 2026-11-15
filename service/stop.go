package service

import (
	"context"
	"fmt"

	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
)

func (svc *service) StopApp(ctx context.Context) {
	if svc.appCancelFn == nil {
		return
	}
	logger.Logger.Info().Msg("Stopping app...")
	svc.appCancelFn()

	shutdownGroup := svc.shutdownGroup
	nostrGroup := svc.nostrGroup

	done := make(chan error, 1)
	go func() {
		err := shutdownGroup.Wait()
		if nostrGroup != nil {
			if nErr := nostrGroup.Wait(); nErr != nil && err == nil {
				err = nErr
			}
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Error while stopping app subsystems")
		}
		logger.Logger.Info().Msg("app stopped")
	case <-ctx.Done():
		logger.Logger.Warn().Msg("Shutdown deadline exceeded waiting for app subsystems to stop; continuing shutdown")
	}
}

func (svc *service) stopLNClient() {
	if svc.lnClient == nil {
		return
	}
	lnClient := svc.lnClient
	svc.lnClient = nil

	logger.Logger.Info().Msg("Shutting down FLN client")
	err := lnClient.Shutdown()
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to stop FLN client")
		svc.eventPublisher.Publish(&events.Event{
			Event: "nwc_node_stop_failed",
			Properties: map[string]interface{}{
				"error": fmt.Sprintf("%v", err),
			},
		})
		return
	}
	logger.Logger.Info().Msg("Publishing node shutdown event")
	svc.eventPublisher.Publish(&events.Event{
		Event: "nwc_node_stopped",
	})
	logger.Logger.Info().Msg("FLN client stopped successfully")
}
