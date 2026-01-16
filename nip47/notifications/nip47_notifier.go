package notifications

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/nip47/permissions"
	nostrmodels "github.com/flokiorg/lokihub/nostr/models"
	"github.com/flokiorg/lokihub/service/keys"
	"github.com/nbd-wtf/go-nostr"
	"gorm.io/gorm"
)

type Nip47Notifier struct {
	pool           nostrmodels.SimplePool
	cfg            config.Config
	keys           keys.Keys
	db             *gorm.DB
	permissionsSvc permissions.PermissionsService
}

func NewNip47Notifier(pool nostrmodels.SimplePool, db *gorm.DB, cfg config.Config, keys keys.Keys, permissionsSvc permissions.PermissionsService) *Nip47Notifier {
	return &Nip47Notifier{
		pool:           pool,
		cfg:            cfg,
		db:             db,
		permissionsSvc: permissionsSvc,
		keys:           keys,
	}
}

func (notifier *Nip47Notifier) ConsumeEvent(ctx context.Context, event *events.Event) error {
	switch event.Event {
	case "nwc_payment_received":
		transaction, ok := event.Properties.(*db.Transaction)
		if !ok {
			logger.Logger.Error().Interface("event", event).Msg("Failed to cast event")
			return errors.New("failed to cast event")
		}

		notification := PaymentReceivedNotification{
			Transaction: *models.ToNip47Transaction(transaction),
		}

		notifier.notifySubscribers(ctx, &Notification{
			Notification:     notification,
			NotificationType: PAYMENT_RECEIVED_NOTIFICATION,
		}, nostr.Tags{}, transaction.AppId)

	case "nwc_payment_sent":
		transaction, ok := event.Properties.(*db.Transaction)
		if !ok {
			logger.Logger.Error().Interface("event", event).Msg("Failed to cast event")
			return errors.New("failed to cast event")
		}

		notification := PaymentSentNotification{
			Transaction: *models.ToNip47Transaction(transaction),
		}

		notifier.notifySubscribers(ctx, &Notification{
			Notification:     notification,
			NotificationType: PAYMENT_SENT_NOTIFICATION,
		}, nostr.Tags{}, transaction.AppId)

	case "nwc_hold_invoice_accepted":
		dbTransaction, ok := event.Properties.(*db.Transaction)
		if !ok {
			logger.Logger.Error().Interface("event", event).Msg("Failed to cast event properties to db.Transaction for hold invoice accepted")
			return errors.New("failed to cast event")
		}

		nip47Transaction := models.ToNip47Transaction(dbTransaction)

		notification := HoldInvoiceAcceptedNotification{
			Transaction: *nip47Transaction,
		}

		notifier.notifySubscribers(ctx, &Notification{
			Notification:     notification,
			NotificationType: HOLD_INVOICE_ACCEPTED_NOTIFICATION,
		}, nostr.Tags{}, dbTransaction.AppId)
	}
	return nil
}

func (notifier *Nip47Notifier) notifySubscribers(ctx context.Context, notification *Notification, tags nostr.Tags, appId *uint) error {
	apps := []db.App{}

	// TODO: join apps and permissions
	err := notifier.db.Find(&apps).Error
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to list apps")
		return errors.New("failed to list apps")
	}

	for _, app := range apps {
		if app.Isolated && (appId == nil || app.ID != *appId) {
			continue
		}

		hasPermission, _, _ := notifier.permissionsSvc.HasPermission(&app, constants.NOTIFICATIONS_SCOPE)
		if !hasPermission {
			continue
		}

		appWalletPrivKey := notifier.keys.GetNostrSecretKey()
		if app.WalletPubkey != nil {
			appWalletPrivKey, err = notifier.keys.GetAppWalletKey(app.ID)
			if err != nil {
				logger.Logger.Error().Err(err).
					Interface("notification", notification).
					Uint("appId", app.ID).
					Msg("error deriving child key")
				return errors.New("failed to derive child key")
			}
		}

		appWalletPubKey, err := nostr.GetPublicKey(appWalletPrivKey)
		if err != nil {
			logger.Logger.Error().Err(err).
				Interface("notification", notification).
				Uint("appId", app.ID).
				Msg("Failed to calculate app wallet pub key")
			return errors.New("failed to calculate app wallet pubkey")
		}

		err = notifier.notifySubscriber(ctx, &app, notification, tags, appWalletPubKey, appWalletPrivKey, constants.ENCRYPTION_TYPE_NIP04)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("failed to notify subscriber (NIP-04)")
			return err
		}
		err = notifier.notifySubscriber(ctx, &app, notification, tags, appWalletPubKey, appWalletPrivKey, constants.ENCRYPTION_TYPE_NIP44_V2)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("failed to notify subscriber (NIP-44)")
			return err
		}
	}
	return nil
}

func (notifier *Nip47Notifier) notifySubscriber(ctx context.Context, app *db.App, notification *Notification, tags nostr.Tags, appWalletPubKey, appWalletPrivKey string, encryption string) error {
	logger.Logger.Debug().
		Interface("notification", notification).
		Uint("appId", app.ID).
		Str("encryption", encryption).
		Msg("Notifying subscriber")

	var err error

	payloadBytes, err := json.Marshal(notification)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("notification", notification).
			Uint("appId", app.ID).
			Str("encryption", encryption).
			Msg("Failed to stringify notification")
		return err
	}

	nip47Cipher, err := cipher.NewNip47Cipher(encryption, app.AppPubkey, appWalletPrivKey)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("notification", notification).
			Uint("appId", app.ID).
			Str("encryption", encryption).
			Msg("Failed to initialize cipher")
		return err
	}

	msg, err := nip47Cipher.Encrypt(string(payloadBytes))
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("notification", notification).
			Uint("appId", app.ID).
			Str("encryption", encryption).
			Msg("Failed to encrypt notification payload")
		return err
	}

	allTags := nostr.Tags{[]string{"p", app.AppPubkey}}
	allTags = append(allTags, tags...)

	event := &nostr.Event{
		PubKey:    appWalletPubKey,
		CreatedAt: nostr.Now(),
		Kind:      models.NOTIFICATION_KIND,
		Tags:      allTags,
		Content:   msg,
	}

	if encryption == constants.ENCRYPTION_TYPE_NIP04 {
		event.Kind = models.LEGACY_NOTIFICATION_KIND
	}

	err = event.Sign(appWalletPrivKey)
	if err != nil {
		logger.Logger.Error().Err(err).
			Interface("notification", notification).
			Uint("appId", app.ID).
			Str("encryption", encryption).
			Msg("Failed to sign event")
		return err
	}

	publishResultChannel := notifier.pool.PublishMany(ctx, notifier.cfg.GetRelayUrls(), *event)

	publishSuccessful := false
	for result := range publishResultChannel {
		if result.Error == nil {
			publishSuccessful = true
		} else {
			logger.Logger.Error().Err(result.Error).
				Interface("notification", notification).
				Uint("appId", app.ID).
				Str("relay", result.RelayURL).
				Msg("failed to publish notification to relay")
		}
	}

	if !publishSuccessful {
		logger.Logger.Error().Err(err).
			Interface("notification", notification).
			Uint("appId", app.ID).
			Str("encryption", encryption).
			Msg("Failed to publish notification")
		return err
	}
	logger.Logger.Debug().
		Uint("appId", app.ID).
		Str("encryption", encryption).
		Msg("Published notification event")
	return nil
}
