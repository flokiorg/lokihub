package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/pkg/version"
	"github.com/flokiorg/lokihub/swaps"
	nostrlsps5 "github.com/flowgate-lsp/nostr-lsps5"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient/lnd"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/lsps/manager"
	lspsnostr "github.com/flokiorg/lokihub/lsps/nostr"
)

func (svc *service) ReloadNostr() error {
	logger.Logger.Info().Msg("Reloading Nostr service...")
	// Stop existing Nostr service
	if svc.nostrCancelFn != nil {
		svc.nostrCancelFn()
	}

	if svc.lnClient == nil {
		logger.Logger.Info().Msg("LNClient not started, skipping Nostr reload")
		return nil
	}

	ctx, cancelFn := context.WithCancel(svc.ctx)
	svc.nostrCancelFn = cancelFn

	return svc.startNostr(ctx)
}

func (svc *service) startNostr(ctx context.Context) error {
	relayUrls := svc.cfg.GetRelayUrls()
	if len(relayUrls) == 0 {
		return errors.New("No relay URLs found")
	}

	npub, err := nip19.EncodePublicKey(svc.keys.GetNostrPublicKey())
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Error converting nostr privkey to pubkey")
		return err
	}

	logger.Logger.Info().
		Str("npub", npub).
		Str("hex", svc.keys.GetNostrPublicKey()).
		Str("version", version.Tag).
		Interface("relay_urls", relayUrls).
		Msg("Starting Lokihub")

	// To debug go-nostr, run with -tags "debug dev" (dev tag so FLND build doesn't break with debug tag set)
	// go run -tags "debug dev" -ldflags="-X 'github.com/flokiorg/lokihub/pkg/version.Tag=v1.20.0'" cmd/http/main.go
	if logger.Logger.GetLevel() >= 4 {
		nostr.InfoLogger.SetOutput(logger.Writer)
		nostr.DebugLogger.SetOutput(logger.Writer)
	}

	// Start infinite loop which will be only broken by canceling ctx (SIGINT)
	pool := nostr.NewSimplePool(ctx, nostr.WithRelayOptions(
		nostr.WithNoticeHandler(svc.noticeHandler),
		nostr.WithRequestHeader(http.Header{
			"User-Agent": {"Lokihub/" + version.Tag},
		}),
	))

	// initially try connect to relays (if hub has no apps, pool won't connect to relays by default)
	for _, relayUrl := range svc.cfg.GetRelayUrls() {
		_, err := pool.EnsureRelay(relayUrl)
		if err != nil {
			logger.Logger.Error().Err(err).Str("relay_url", relayUrl).Msg("failed to initially connect to relay")
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				svc.relayStatuses = nil
				for _, relayUrl := range svc.cfg.GetRelayUrls() {
					normalizedUrl := nostr.NormalizeURL(relayUrl)
					relay, ok := pool.Relays.Load(normalizedUrl)
					online := ok && relay != nil && relay.IsConnected()

					// Force reconnection if offline
					if !online {
						go pool.EnsureRelay(relayUrl)
					}

					svc.relayStatuses = append(svc.relayStatuses, RelayStatus{
						Url:    relayUrl,
						Online: online,
					})
				}
				time.Sleep(10 * time.Second)
			}
		}
	}()

	svc.nip47Service.StartNotifier(ctx, pool)
	svc.nip47Service.StartNip47InfoPublisher(ctx, pool, svc.lnClient)

	// Start LSPS5 listener
	svc.lsps5Listener = lspsnostr.NewListener(svc.keys, svc.cfg, svc.eventPublisher, func() []string {
		if svc.liquidityManager == nil {
			return nil
		}
		lsps, err := svc.liquidityManager.GetSelectedLSPs()
		if err != nil {
			logger.Logger.Warn().Err(err).Msg("Failed to get trusted LSPs for Nostr listener")
			return nil
		}
		var keys []string
		for _, l := range lsps {
			if l.NostrPubkey != "" {
				keys = append(keys, l.NostrPubkey)
			}
		}
		return keys
	})
	if err := svc.lsps5Listener.Start(ctx, pool); err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to start LSPS5 Nostr listener")
	}
	// Register LSPS5 event consumer
	lspsConsumer := &lspsEventConsumer{svc: svc}
	svc.eventPublisher.RegisterSubscriber(lspsConsumer)

	// register a subscriber for events of "nwc_app_created" which handles creation of nostr subscription for new app
	createAppEventListener := &createAppConsumer{svc: svc, pool: pool}
	svc.eventPublisher.RegisterSubscriber(createAppEventListener)

	// register a subscriber for events of "nwc_app_updated" which handles re-publishing of nip47 event info
	updateAppEventListener := &updateAppConsumer{svc: svc}
	svc.eventPublisher.RegisterSubscriber(updateAppEventListener)

	// start each app wallet subscription which have a child derived wallet key
	svc.startAllExistingAppsWalletSubscriptions(ctx, pool)

	// check if there are still legacy apps in DB
	var legacyAppCount int64
	result := svc.db.Model(&db.App{}).Where("wallet_pubkey IS NULL").Count(&legacyAppCount)
	if result.Error != nil {
		logger.Logger.Error().Err(result.Error).Msg("Failed to count Legacy Apps")
	}
	if legacyAppCount > 0 {
		go func() {
			logger.Logger.Info().Interface("legacy_app_count", legacyAppCount).Msg("Starting legacy app subscription")
			// legacy single wallet subscription - only subscribe once for all legacy apps
			// to ensure we do not get duplicate events
			svc.startAppWalletSubscription(ctx, pool, svc.keys.GetNostrPublicKey())
		}()
	}

	go func() {
		<-ctx.Done()
		logger.Logger.Info().Msg("Main context cancelled, exiting...")

		pool.Close("exiting")
		logger.Logger.Info().Msg("Relay subroutine ended")

		svc.eventPublisher.RemoveSubscriber(createAppEventListener)
		svc.eventPublisher.RemoveSubscriber(updateAppEventListener)
	}()

	return nil
}

// In case the relay somehow loses events or the hub updates with
// new capabilities, we re-publish info events for all apps on startup
// to ensure that they are retrievable for all connections
func (svc *service) publishAllAppInfoEvents() {
	func() {
		var legacyAppCount int64
		result := svc.db.Model(&db.App{}).Where("wallet_pubkey IS NULL").Count(&legacyAppCount)
		if result.Error != nil {
			logger.Logger.Error().Err(result.Error).Msg("Failed to fetch App records with empty WalletPubkey")
			return
		}
		if legacyAppCount > 0 {
			logger.Logger.Debug().Interface("legacy_app_count", legacyAppCount).Msg("Enqueuing publish of legacy info event")
			for _, relayUrl := range svc.cfg.GetRelayUrls() {
				svc.nip47Service.EnqueueNip47InfoPublishRequest(0 /* unused */, svc.keys.GetNostrPublicKey(), svc.keys.GetNostrSecretKey(), relayUrl)
			}
		}
	}()

	var apps []db.App
	result := svc.db.Where("wallet_pubkey IS NOT NULL").Find(&apps)
	if result.Error != nil {
		logger.Logger.Error().Err(result.Error).Msg("Failed to fetch App records with non-empty WalletPubkey")
		return
	}

	for _, app := range apps {
		func(app db.App) {
			// queue info event publish request for all existing apps
			walletPrivKey, err := svc.keys.GetAppWalletKey(app.ID)
			if err != nil {
				logger.Logger.Error().Err(err).
					Uint("app_id", app.ID).
					Msg("Could not get app wallet key")
				return
			}
			logger.Logger.Debug().Interface("app_id", app.ID).Msg("Enqueuing publish of app info event")
			for _, relayUrl := range svc.cfg.GetRelayUrls() {
				svc.nip47Service.EnqueueNip47InfoPublishRequest(app.ID, *app.WalletPubkey, walletPrivKey, relayUrl)
			}
		}(app)
	}
}

func (svc *service) startAllExistingAppsWalletSubscriptions(ctx context.Context, pool *nostr.SimplePool) {
	var apps []db.App
	result := svc.db.Where("wallet_pubkey IS NOT NULL").Find(&apps)
	if result.Error != nil {
		logger.Logger.Error().Err(result.Error).Msg("Failed to fetch App records with non-empty WalletPubkey")
		return
	}

	for _, app := range apps {
		go func(app db.App) {
			svc.startAppWalletSubscription(ctx, pool, *app.WalletPubkey)
		}(app)
	}
}

func (svc *service) startAppWalletSubscription(ctx context.Context, pool *nostr.SimplePool, appWalletPubKey string) error {

	logger.Logger.Info().Str("wallet", appWalletPubKey).Msg("Subscribing to events")

	filter := nostr.Filter{
		Tags:  nostr.TagMap{"p": []string{appWalletPubKey}},
		Kinds: []int{models.REQUEST_KIND},
	}

	for {
		subCtx, cancelSubscription := context.WithCancel(ctx)
		eventsChannel := pool.SubscribeMany(subCtx, svc.cfg.GetRelayUrls(), filter)

		// register a subscriber for "nwc_app_deleted" events, which handles
		// cancelling the nostr subscription and nip47 info event deletion
		deleteAppSubscriber := deleteAppConsumer{
			cancelSubscription: cancelSubscription,
			walletPubkey:       appWalletPubKey,
			svc:                svc,
			pool:               pool,
		}

		svc.eventPublisher.RegisterSubscriber(&deleteAppSubscriber)

		err := svc.watchSubscription(subCtx, pool, eventsChannel)

		svc.eventPublisher.RemoveSubscriber(&deleteAppSubscriber)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("got an error from the relay while listening to subscription, resubscribing")
			time.Sleep(3 * time.Second)
			continue
		}
		break
	}
	return nil
}

func (svc *service) watchSubscription(ctx context.Context, pool *nostr.SimplePool, eventsChannel chan nostr.RelayEvent) error {
	eventsChannelClosed := make(chan struct{})
	go func() {
		// loop through incoming events
		for event := range eventsChannel {
			select {
			case <-ctx.Done():
				return
			default:
				go svc.nip47Service.HandleEvent(ctx, pool, event.Event, svc.lnClient)
			}
		}
		logger.Logger.Debug().Msg("Relay subscription events channel ended")
		eventsChannelClosed <- struct{}{}
	}()

	select {
	case <-ctx.Done():
		logger.Logger.Info().Msg("Exiting subscription due to context exit...")
		return nil
	case <-eventsChannelClosed:
		// in go-nostr pool, currently if the relay sends a close that is not "auth-required:"
		// this will trigger closing the subscription channel. We return an error to trigger a resubscribe.
		logger.Logger.Info().Msg("Subscription was exited abnormally")
		return errors.New("subscription exited abnormally")
	}
}

func (svc *service) StartApp(encryptionKey string) error {
	defer func() {
		svc.startupState = ""
	}()

	svc.startupState = "Initializing"

	if svc.lnClient != nil {
		return errors.New("app already started")
	}
	if !svc.cfg.CheckUnlockPassword(encryptionKey) {
		logger.Logger.Error().Msg("Invalid password")
		return errors.New("invalid password")
	}

	err := svc.cfg.Unlock(encryptionKey)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to unlock config")
		return err
	}

	err = svc.keys.Init(svc.cfg, encryptionKey)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to init nostr keys")
		return err
	}

	ctx, cancelFn := context.WithCancel(svc.ctx)

	svc.startupState = "Connecting to Node"
	err = svc.launchLNBackend(ctx, encryptionKey)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to connect to FLN backend")
		svc.eventPublisher.Publish(&events.Event{
			Event: "nwc_node_start_failed",
		})
		cancelFn()
		return err
	}

	svc.swapsService = swaps.NewSwapsService(ctx, svc.db, svc.cfg, svc.keys, svc.eventPublisher, svc.lnClient, svc.transactionsService)

	// Initialize and start LiquidityManager (LSPS)
	// Initialize and start LiquidityManager (LSPS)
	lspManager := manager.NewLSPManager(svc.db)
	lmCfg := manager.NewManagerConfig(svc.lnClient, lspManager, svc.eventPublisher, svc.cfg)

	// Inject webhook configuration logic to allow LiquidityManager to auto-register
	lmCfg.GetWebhookConfig = func() (string, string) {
		// Try Nostr transport first
		if constants.DEFAULT_ENABLE_NOSTR_NOTIFICATIONS {
			if svc.GetKeys() != nil {
				privKey := svc.GetKeys().GetNostrSecretKey()
				if privKey == "" {
					return "", ""
				}

				lsps5 := nostrlsps5.NewLSPS5(privKey)
				uri, err := lsps5.GenerateLsp5Uri(svc.cfg.GetRelayUrls())
				if err == nil {
					return uri, "nostr"
				}
				logger.Logger.Warn().Err(err).Msg("Failed to generate LSPS5 URI via library")
			}
		}
		// Possible HTTP fallback in the future
		return "", ""
	}

	lm, err := manager.NewLiquidityManager(lmCfg)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to initialize LiquidityManager")
		// We don't fail startup for this yet, but we should log it
	} else {
		svc.liquidityManager = lm
		svc.transactionsService.SetLiquidityManager(lm)
		if err := lm.Start(ctx); err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to start LiquidityManager")
		} else {
			logger.Logger.Info().Msg("LiquidityManager started")
			// Sync system LSPs asynchronously
			// Start background sync service
			go func() {
				url := svc.cfg.GetLokihubServicesURL()
				lm.StartSyncService(ctx, url)
			}()
		}
	}

	svc.publishAllAppInfoEvents()

	svc.startupState = "Connecting To Relay"
	err = svc.startNostr(ctx)
	if err != nil {
		cancelFn()
		return err
	}

	svc.appCancelFn = cancelFn

	return nil
}

func (svc *service) launchLNBackend(ctx context.Context, encryptionKey string) error {
	if svc.lnClient != nil {
		logger.Logger.Error().Msg("LNClient already started")
		return errors.New("LNClient already started")
	}

	svc.wg.Add(1)
	go func() {
		// ensure the LNClient is stopped properly before exiting
		<-ctx.Done()
		svc.stopLNClient()
	}()

	logger.Logger.Info().Msgf("Connecting to FLN Backend: %s", config.LNDBackendType)

	LNDAddress, _ := svc.cfg.Get("LNDAddress", encryptionKey)
	LNDCertHex, _ := svc.cfg.Get("LNDCertHex", encryptionKey)
	LNDMacaroonHex, _ := svc.cfg.Get("LNDMacaroonHex", encryptionKey)

	lnClient, err := lnd.NewLNDService(ctx, svc.eventPublisher, LNDAddress, LNDCertHex, LNDMacaroonHex)

	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to connect to FLN backend")
		return err
	}

	// TODO: call a method on the LNClient here to check the LNClient is actually connectable,
	// (e.g. lnClient.CheckConnection()) Rather than it being a side-effect
	// in the LNClient init function

	svc.lnClient = lnClient
	info, err := lnClient.GetInfo(ctx)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch node info")
	}
	if info != nil {
		svc.eventPublisher.SetGlobalProperty("node_id", info.Pubkey)
		svc.eventPublisher.SetGlobalProperty("network", info.Network)
	}

	// Mark that the node has successfully started
	// This will ensure the user cannot go through the setup again
	err = svc.cfg.SetUpdate("NodeLastStartTime", strconv.FormatInt(time.Now().Unix(), 10), "")
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to set last node start time")
	}

	svc.eventPublisher.Publish(&events.Event{
		Event: "nwc_node_started",
		Properties: map[string]interface{}{
			"node_type": config.LNDBackendType,
		},
	})

	return nil
}
