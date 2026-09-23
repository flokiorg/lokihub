package service

import (
	"context"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
	"github.com/nbd-wtf/go-nostr"
)

type updateAppConsumer struct {
	events.EventSubscriber
	svc *service
}

// When a app is updated, re-publish the nip47 info event
func (s *updateAppConsumer) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	if event.Event != "nwc_app_updated" {
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
	walletPrivKey, err := s.svc.keys.GetAppWalletKey(id)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("id", id).Msg("Failed to calculate app wallet priv key")
		return
	}
	walletPubKey, err := nostr.GetPublicKey(walletPrivKey)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("id", id).Msg("Failed to calculate app wallet pub key")
		return
	}

	// Kind is needed to decide whether this app advertises itself at all — a
	// cash bill never does (see db.PublishesNip47InfoEvent), and re-publishing
	// on update would undo the suppression at creation time.
	app := db.App{}
	if err := s.svc.db.First(&app, &db.App{ID: id}).Error; err != nil {
		logger.Logger.Error().Err(err).Uint("id", id).Msg("Failed to find app for id")
		return
	}

	if s.svc.keys.GetNostrPublicKey() != walletPubKey && db.PublishesNip47InfoEvent(app.Kind) {
		// only need to re-publish the nip47 event info if it is not a legacy app connection (shared wallet pubkey)
		// (legacy app connection can be used for multiple apps - so it cannot be app-specific)
		for _, relayUrl := range s.svc.cfg.GetRelayUrls() {
			s.svc.nip47Service.EnqueueNip47InfoPublishRequest(id, walletPubKey, walletPrivKey, relayUrl)
		}
	}
}
