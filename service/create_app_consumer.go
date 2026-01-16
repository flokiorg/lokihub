package service

import (
	"context"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
)

type createAppConsumer struct {
	events.EventSubscriber
	svc  *service
	pool *nostr.SimplePool
}

// When a new app is created, subscribe to it on the relay
func (s *createAppConsumer) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	if event.Event != "nwc_app_created" {
		return
	}

	properties, ok := event.Properties.(map[string]interface{})
	if !ok {
		logger.Logger.Error().Interface("event", event).Msg("Failed to cast event.Properties to map")
		return
	}
	id, ok := properties["id"].(uint)
	if !ok {
		logger.Logger.Error().Interface("event", event).Msg("Failed to get app id")
		return
	}

	app := db.App{}
	err := s.svc.db.First(&app, &db.App{
		ID: id,
	}).Error
	if err != nil {
		logger.Logger.Error().Fields(map[string]interface{}{
			"id": id,
		}).Err(err).Msg("Failed to find app for id")
		return
	}

	walletPrivKey, err := s.svc.keys.GetAppWalletKey(id)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to calculate app wallet priv key")
		return
	}
	walletPubKey, err := nostr.GetPublicKey(walletPrivKey)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to calculate app wallet pub key")
		return
	}
	for _, relayUrl := range s.svc.cfg.GetRelayUrls() {
		s.svc.nip47Service.EnqueueNip47InfoPublishRequest(id, walletPubKey, walletPrivKey, relayUrl)
	}

	go s.svc.startAppWalletSubscription(ctx, s.pool, walletPubKey)
}
