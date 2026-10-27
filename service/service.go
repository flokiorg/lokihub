package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adrg/xdg"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/appstore"
	"github.com/flokiorg/lokihub/appversion"
	"github.com/flokiorg/lokihub/db/migrations"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/loki"
	"github.com/flokiorg/lokihub/swaps"
	"github.com/flokiorg/lokihub/transactions"

	"github.com/flokiorg/lokihub/cashorder"
	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/cashwithdraw"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/manager"
	lspsnostr "github.com/flokiorg/lokihub/lsps/nostr"
	"github.com/flokiorg/lokihub/nip47"
)

type service struct {
	cfg config.Config

	db                  *gorm.DB
	lnClient            lnclient.LNClient
	transactionsService transactions.TransactionsService
	swapsService        swaps.SwapsService
	lokiSvc             loki.LokiService
	appStoreSvc         appstore.Service
	eventPublisher      events.EventPublisher
	ctx                 context.Context
	shutdownGroup       *errgroup.Group
	nostrGroup          *errgroup.Group
	// walletRegistry backs the shared NIP-47 subscription used when
	// config.TrustedNwcRelay is on; nil when the per-wallet path is in use.
	walletRegistry *walletRegistry
	// droppedRequestEvents counts events the gate discarded, summarised on a
	// timer rather than logged per event.
	droppedRequestEvents atomic.Int64
	nip47Service         nip47.Nip47Service
	// withdraw serves on-chain withdrawals (service/withdraw.go).
	withdraw *cashwithdraw.Service
	// withdrawEngine pays the payouts (service/withdraw.go).
	withdrawEngine   *cashwithdraw.Engine
	socialCache      *nostrSocialCache
	lsps5Listener    *lspsnostr.Listener
	liquidityManager *manager.LiquidityManager
	appCancelFn      context.CancelFunc
	nostrCancelFn    context.CancelFunc
	keys             keys.Keys
	relayStatuses    []RelayStatus
	startupState     string
	// privateTransportStatus is set by setPrivateTransportIdentity /
	// setPrivateTransportAnnounceResult (service/private_transport.go) and
	// read by GetPrivateTransportStatus. A pointer so a read never races a
	// concurrent update of the whole snapshot.
	privateTransportStatus atomic.Pointer[PrivateTransportStatus]
	// privateTransportStatusMu serialises the read-modify-write updates, now
	// that the inbox's liveness watcher updates the snapshot concurrently with
	// startup.
	privateTransportStatusMu sync.Mutex
	// orders takes order payments in and mints them (NIP-CO).
	orders *cashorder.Engine
}

func NewService(ctx context.Context) (*service, error) {
	// Load config from environment variables / .GetEnv() file. Missing
	// .env is fine — config also comes from real environment variables.
	_ = godotenv.Load(".env")
	appConfig := &config.AppConfig{}
	err := envconfig.Process("", appConfig)
	if err != nil {
		return nil, err
	}

	logger.Init(appConfig.LogLevel)
	logger.Logger.Info().Msg("Lokihub " + appversion.Tag)

	if appConfig.Workdir == "" {
		appConfig.Workdir = filepath.Join(xdg.DataHome, "/lokihub")
		logger.Logger.Info().Interface("workdir", appConfig.Workdir).Msg("No workdir specified, using default")
	}
	// make sure workdir exists; it holds the wallet DB and config secrets,
	// so keep it inaccessible to other local users.
	if err := os.MkdirAll(appConfig.Workdir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create workdir: %w", err)
	}

	if appConfig.LogToFile {
		err = logger.AddFileLogger(appConfig.Workdir)
		if err != nil {
			return nil, err
		}
	}

	err = finishRestoreNode(appConfig.Workdir)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("failed to restore backup")
		return nil, err
	}

	// If DATABASE_URI is a URI or a path, leave it unchanged.
	// If it only contains a filename, prepend the workdir.
	if !strings.HasPrefix(appConfig.DatabaseUri, "file:") {
		databasePath, _ := filepath.Split(appConfig.DatabaseUri)
		if databasePath == "" {
			appConfig.DatabaseUri = filepath.Join(appConfig.Workdir, appConfig.DatabaseUri)
		}
	}

	gormDB, err := db.NewDB(appConfig.DatabaseUri, appConfig.LogDBQueries)
	if err != nil {
		return nil, err
	}
	err = migrations.Migrate(gormDB)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Refusing to start: the database cannot be used")
		return nil, err
	}

	cfg, err := config.NewConfig(appConfig, gormDB)
	if err != nil {
		return nil, err
	}

	// The horizon H only ever increases (NIP-CASH §Deadlines): the stored H is
	// raised to the configured one, and a lower configured H is ignored.
	horizon, err := cashrequest.RaiseHorizon(gormDB, cfg.PrivateEnvelopeLimits().MaxRequestAgeSeconds, time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to store the horizon: %w", err)
	}
	if configured := int64(appConfig.CashMaxRequestAgeSecs); configured > 0 && configured < horizon {
		logger.Logger.Error().Int64("configured", configured).Int64("in_force", horizon).
			Msg("CASH_MAX_REQUEST_AGE_SECS is below the stored horizon, which can only increase; keeping the stored one")
	}

	// write auto unlock password from env to user config
	if appConfig.AutoUnlockPassword != "" {
		err = cfg.SetUpdate("AutoUnlockPassword", appConfig.AutoUnlockPassword, "")
		if err != nil {
			return nil, err
		}
	}
	autoUnlockPassword, err := cfg.Get("AutoUnlockPassword", "")
	if err != nil {
		return nil, err
	}

	eventPublisher := events.NewEventPublisher()

	keys := keys.NewKeys()

	lokiSvc := loki.NewLokiService(cfg)

	transactionsSvc := transactions.NewTransactionsService(gormDB, eventPublisher)

	socialCache := NewNostrSocialCache(cfg)
	socialCache.inboxKey = func() (string, error) {
		return keys.GetPrivateTransportKey(privateTransportKeyIndex)
	}
	socialCache.retiredInboxKeys = func() []string { return retiredInboxKeys(gormDB, keys) }
	socialCache.onResolved = func(provider, d string, set *FollowSet) {
		ApplyFollowSetResolution(gormDB, eventPublisher, provider, d, set.IsMember, time.Now())
	}

	svc := &service{
		cfg:            cfg,
		ctx:            ctx,
		eventPublisher: eventPublisher,
		lokiSvc:        lokiSvc,

		nip47Service:        nip47.NewNip47Service(gormDB, cfg, keys, eventPublisher, socialCache),
		socialCache:         socialCache,
		transactionsService: transactionsSvc,
		db:                  gormDB,
		keys:                keys,
	}

	svc.nip47Service.SetPrivateIdentity(svc.privateJoinIdentity)
	// A Circle Wallet payment the node reports accepted is settled by the Hub
	// once re-checked, which needs the node.
	svc.transactionsService.SetLNClientProvider(svc.GetLNClient)
	svc.orders = svc.newOrderEngine(gormDB, keys, cfg, eventPublisher)
	svc.setupWithdraw()

	eventPublisher.RegisterSubscriber(svc.transactionsService)
	eventPublisher.RegisterSubscriber(svc.nip47Service)

	eventPublisher.RegisterSubscriber(&paymentForwardedConsumer{
		db: gormDB,
	})
	eventPublisher.RegisterSubscriber(&cashRedeemOutcomeConsumer{
		appsService:    apps.NewAppsService(gormDB, eventPublisher, keys, cfg),
		db:             gormDB,
		eventPublisher: eventPublisher,
	})

	svc.appStoreSvc = appstore.NewAppStoreService(cfg)
	svc.appStoreSvc.Start()

	eventPublisher.Publish(&events.Event{
		Event: "nwc_started",
		Properties: map[string]interface{}{
			"version": appversion.Tag,
		},
	})

	if appConfig.GoProfilerAddr != "" {
		startProfiler(ctx, appConfig.GoProfilerAddr)
	}

	if autoUnlockPassword != "" {
		nodeLastStartTime, _ := cfg.Get("NodeLastStartTime", "")
		if nodeLastStartTime != "" {
			if err := svc.StartApp(autoUnlockPassword); err != nil {
				logger.Logger.Error().Err(err).Msg("Auto-unlock StartApp failed; node will require manual start")
			}
		}
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				time.Sleep(10 * time.Minute)
				svc.removeExcessEvents()
			}
		}
	}()

	return svc, nil
}

func (svc *service) noticeHandler(notice string) {
	logger.Logger.Info().Str("notice", notice).Msg("Received a notice")
}

func finishRestoreNode(workDir string) error {
	restoreDir := filepath.Join(workDir, "restore")
	if restoreDirStat, err := os.Stat(restoreDir); err == nil && restoreDirStat.IsDir() {
		logger.Logger.Info().Str("restoreDir", restoreDir).Msg("Restore directory found. Finishing Node restore")

		existingFiles, err := os.ReadDir(restoreDir)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to read WORK_DIR")
			return err
		}

		for _, file := range existingFiles {
			if file.Name() != "restore" {
				err = os.RemoveAll(filepath.Join(workDir, file.Name()))
				if err != nil {
					logger.Logger.Error().Err(err).Str("filename", file.Name()).Msg("Failed to remove file")
					return err
				}
				logger.Logger.Info().Str("filename", file.Name()).Msg("removed file")
			}
		}

		files, err := os.ReadDir(restoreDir)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to read restore directory")
			return err
		}
		for _, file := range files {
			err = os.Rename(filepath.Join(restoreDir, file.Name()), filepath.Join(workDir, file.Name()))
			if err != nil {
				logger.Logger.Error().Err(err).Str("filename", file.Name()).Msg("Failed to move file")
				return err
			}
			logger.Logger.Info().Str("filename", file.Name()).Msg("copied file from restore directory")
		}
		err = os.RemoveAll(restoreDir)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to remove restore directory")
			return err
		}
		logger.Logger.Info().Interface("restoreDir", restoreDir).Msg("removed restore directory")
	}
	return nil
}

func (svc *service) Shutdown(ctx context.Context) {
	svc.StopApp(ctx)
	svc.eventPublisher.PublishSync(&events.Event{
		Event: "nwc_stopped",
	})
	if err := db.Stop(svc.db); err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to close database connection")
	}
}

func (svc *service) GetDB() *gorm.DB {
	return svc.db
}

func (svc *service) GetConfig() config.Config {
	return svc.cfg
}

func (svc *service) GetLokiSvc() loki.LokiService {
	return svc.lokiSvc
}

func (svc *service) GetNip47Service() nip47.Nip47Service {
	return svc.nip47Service
}

func (svc *service) RefreshFollowSet(ctx context.Context, provider, d string) (*FollowSet, error) {
	return svc.socialCache.RefreshFollowSet(ctx, provider, d)
}

func (svc *service) FollowSet(ctx context.Context, provider, d string) (*FollowSet, error) {
	return svc.socialCache.FollowSet(ctx, provider, d)
}

func (svc *service) PeekFollowSet(provider, d string) (*FollowSet, bool) {
	return svc.socialCache.PeekFollowSet(provider, d)
}

// WarmGeneralRelays re-connects the shared Nostr pool to the current General
// relay list — call after the user updates that setting.
func (svc *service) WarmGeneralRelays() {
	svc.socialCache.WarmGeneralRelays()
}

func (svc *service) GetEventPublisher() events.EventPublisher {
	return svc.eventPublisher
}

func (svc *service) GetLNClient() lnclient.LNClient {
	return svc.lnClient
}

func (svc *service) GetTransactionsService() transactions.TransactionsService {
	return svc.transactionsService
}

func (svc *service) GetSwapsService() swaps.SwapsService {
	return svc.swapsService
}

func (svc *service) GetLiquidityManager() *manager.LiquidityManager {
	return svc.liquidityManager
}

func (svc *service) InitSwapsService() {
	if svc.swapsService != nil {
		return
	}
	if svc.lnClient == nil {
		logger.Logger.Error().Msg("Cannot init swaps service: LNClient not started")
		return
	}
	svc.swapsService = swaps.NewSwapsService(svc.ctx, svc.db, svc.cfg, svc.keys, svc.eventPublisher, svc.lnClient, svc.transactionsService)
}

func (svc *service) GetKeys() keys.Keys {
	return svc.keys
}

func (svc *service) GetRelayStatuses() []RelayStatus {
	return svc.relayStatuses
}

func (svc *service) GetStartupState() string {
	return svc.startupState
}

func (svc *service) GetAppStoreSvc() appstore.Service {
	return svc.appStoreSvc
}

func (svc *service) removeExcessEvents() {
	const (
		maxEvents         = 1000
		maxEventsToDelete = 5000
		minEventsToDelete = 100
	)

	var total int64
	if err := svc.db.Model(&db.RequestEvent{}).Count(&total).Error; err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to count request events")
		return
	}

	numEventsToDelete := int(total) - maxEvents
	if numEventsToDelete < minEventsToDelete {
		return
	}
	if numEventsToDelete > maxEventsToDelete {
		numEventsToDelete = maxEventsToDelete
	}

	logger.Logger.Debug().Int("amount", numEventsToDelete).Msg("Removing excess events")

	// Delete the numEventsToDelete oldest rows without loading them into memory.
	// The subquery finds the boundary ID: OFFSET (n-1) with ASC order lands on
	// the last row to delete, so WHERE id <= boundary removes exactly n rows.
	startTime := time.Now()
	result := svc.db.Exec(
		"DELETE FROM request_events WHERE id <= (SELECT id FROM request_events ORDER BY id ASC LIMIT 1 OFFSET ?)",
		numEventsToDelete-1,
	)
	if result.Error != nil {
		logger.Logger.Error().Err(result.Error).
			Int("amount", numEventsToDelete).
			Msg("Failed to delete excess request events")
		return
	}
	logger.Logger.Info().
		Int64("amount", result.RowsAffected).
		Float64("duration_seconds", time.Since(startTime).Seconds()).
		Msg("Removed excess events")
}
