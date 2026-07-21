package nip47

import (
	"context"
	"time"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/flokiorg/lokihub/nip47/controllers"
	"github.com/flokiorg/lokihub/nip47/notifications"
	"github.com/flokiorg/lokihub/nip47/permissions"
	nostrmodels "github.com/flokiorg/lokihub/nostr/models"
	"github.com/flokiorg/lokihub/transactions"
	"github.com/nbd-wtf/go-nostr"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

type nip47Service struct {
	permissionsService  permissions.PermissionsService
	transactionsService transactions.TransactionsService
	appsService         apps.AppsService

	nip47NotificationQueue notifications.Nip47NotificationQueue
	nip47InfoPublishQueue  *nip47InfoPublishQueue
	cfg                    config.Config
	keys                   keys.Keys
	db                     *gorm.DB
	eventPublisher         events.EventPublisher
	logger                 zerolog.Logger
	socialCache            controllers.NostrSocialCache
	jitRateLimiter         controllers.RateLimiter
	jitClaimLimiter        controllers.RateLimiter
	circleRateLimiter      controllers.RateLimiter
	identityAuthorityMgr   *apps.IdentityAuthorityManager
}

type Nip47Service interface {
	events.EventSubscriber
	StartNotifier(ctx context.Context, pool *nostr.SimplePool)
	StartNip47InfoPublisher(ctx context.Context, pool *nostr.SimplePool, lnClient lnclient.LNClient)
	HandleEvent(ctx context.Context, pool nostrmodels.SimplePool, event *nostr.Event, lnClient lnclient.LNClient)
	GetNip47Info(ctx context.Context, pool nostrmodels.SimplePool, appWalletPubKey string) (*nostr.Event, error)
	PublishNip47Info(ctx context.Context, pool nostrmodels.SimplePool, appId uint, appWalletPubKey string, appWalletPrivKey string, relayUrl string, lnClient lnclient.LNClient) (*nostr.Event, error)
	PublishNip47InfoDeletion(ctx context.Context, pool nostrmodels.SimplePool, appWalletPubKey string, appWalletPrivKey string, infoEventId string) error
	CreateResponse(initialEvent *nostr.Event, content interface{}, tags nostr.Tags, cipher *cipher.Nip47Cipher, walletPrivKey string) (result *nostr.Event, err error)
	EnqueueNip47InfoPublishRequest(appId uint, appWalletPubKey, appWalletPrivKey, relayUrl string)
}

func NewNip47Service(db *gorm.DB, cfg config.Config, keys keys.Keys, eventPublisher events.EventPublisher, socialCache controllers.NostrSocialCache) *nip47Service {
	return &nip47Service{
		nip47NotificationQueue: notifications.NewNip47NotificationQueue(),
		nip47InfoPublishQueue:  NewNip47InfoPublishQueue(),
		cfg:                    cfg,
		db:                     db,
		permissionsService:     permissions.NewPermissionsService(db, eventPublisher),
		transactionsService:    transactions.NewTransactionsService(db, eventPublisher),
		appsService:            apps.NewAppsService(db, eventPublisher, keys, cfg),
		eventPublisher:         eventPublisher,
		keys:                   keys,
		logger:                 logger.Logger.With().Str("component", "nip47").Logger(),
		socialCache:            socialCache,
		jitRateLimiter:         controllers.NewRateLimiter(),
		jitClaimLimiter:        controllers.NewRateLimiter(),
		circleRateLimiter:      controllers.NewRateLimiter(),
		identityAuthorityMgr:   apps.NewIdentityAuthorityManager(db),
	}
}

func (svc *nip47Service) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	svc.nip47NotificationQueue.AddToQueue(event)
}

// The notifier is decoupled from the notification queue
// so that if Lokihub disconnects from the relay, it will wait to reconnect
// to send notifications rather than dropping them
func (svc *nip47Service) StartNotifier(ctx context.Context, pool *nostr.SimplePool) {
	nip47Notifier := notifications.NewNip47Notifier(pool, svc.db, svc.cfg, svc.keys, svc.permissionsService)
	go func() {
		for {
			select {
			case <-ctx.Done():
				// app exited
				return
			case event := <-svc.nip47NotificationQueue.Channel():
				svc.logger.Debug().Interface("event", event).Msg("Consuming event from notification queue")
				err := nip47Notifier.ConsumeEvent(ctx, event)
				if err != nil {
					svc.logger.Error().Err(err).Interface("event", event).Msg("Failed to consume event from notification queue")
					// wait and then re-add the item to the queue
					time.Sleep(5 * time.Second)
					svc.nip47NotificationQueue.AddToQueue(event)
				}
			}
		}
	}()
}

func (svc *nip47Service) EnqueueNip47InfoPublishRequest(appId uint, appWalletPubKey, appWalletPrivKey, relayUrl string) {
	svc.enqueueNip47InfoPublishRequestWithAttempt(appId, appWalletPubKey, appWalletPrivKey, relayUrl, 0)
}

func (svc *nip47Service) enqueueNip47InfoPublishRequestWithAttempt(appId uint, appWalletPubKey, appWalletPrivKey, relayUrl string, attempt uint32) {
	svc.nip47InfoPublishQueue.AddToQueue(&Nip47InfoPublishRequest{
		AppId:            appId,
		AppWalletPubKey:  appWalletPubKey,
		AppWalletPrivKey: appWalletPrivKey,
		RelayUrl:         relayUrl,
		Attempt:          attempt,
	})
}

// minNip47InfoPublisherWorkers is a floor on the worker pool size so that even
// a single configured relay still gets some concurrency for retries.
const minNip47InfoPublisherWorkers = 4

// StartNip47InfoPublisher runs a small pool of workers draining the publish
// queue concurrently. A single consumer would let one slow/unresponsive relay
// head-of-line-block every other queued publish (including for healthy
// relays and unrelated apps); a bounded pool avoids that without retrying
// faster or waiting longer on any individual publish.
func (svc *nip47Service) StartNip47InfoPublisher(ctx context.Context, pool *nostr.SimplePool, lnClient lnclient.LNClient) {
	workers := max(len(svc.cfg.GetRelayUrls()), minNip47InfoPublisherWorkers)
	for range workers {
		go svc.runNip47InfoPublisherWorker(ctx, pool, lnClient)
	}
}

func (svc *nip47Service) runNip47InfoPublisherWorker(ctx context.Context, pool *nostr.SimplePool, lnClient lnclient.LNClient) {
	for {
		select {
		case <-ctx.Done():
			// relay disconnected
			return
		case req := <-svc.nip47InfoPublishQueue.Channel():
			_, err := svc.PublishNip47Info(ctx, pool, req.AppId, req.AppWalletPubKey, req.AppWalletPrivKey, req.RelayUrl, lnClient)
			if err != nil {
				svc.logger.Error().Err(err).
					Str("wallet_pubkey", req.AppWalletPubKey).
					Str("relay_url", req.RelayUrl).
					Msg("Failed to publish NIP47 info from queue")

				// wait and then re-add the item to the queue, without holding
				// a goroutine (and its stack) parked for the whole backoff
				delay := (5 * time.Duration(req.Attempt+1)) * time.Second
				time.AfterFunc(delay, func() {
					svc.enqueueNip47InfoPublishRequestWithAttempt(req.AppId, req.AppWalletPubKey, req.AppWalletPrivKey, req.RelayUrl, req.Attempt+1)
				})
			}
		}
	}
}
