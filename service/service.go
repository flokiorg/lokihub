package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"

	"github.com/flokiorg/lokihub/appstore"
	"github.com/flokiorg/lokihub/db/migrations"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/loki"
	"github.com/flokiorg/lokihub/swaps"
	"github.com/flokiorg/lokihub/transactions"
	"github.com/flokiorg/lokihub/version"

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
	nip47Service        nip47.Nip47Service
	socialCache         *nostrSocialCache
	lsps5Listener       *lspsnostr.Listener
	liquidityManager    *manager.LiquidityManager
	appCancelFn         context.CancelFunc
	nostrCancelFn       context.CancelFunc
	keys                keys.Keys
	relayStatuses       []RelayStatus
	startupState        string
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
	logger.Logger.Info().Msg("Lokihub " + version.Tag)

	if appConfig.Workdir == "" {
		appConfig.Workdir = filepath.Join(xdg.DataHome, "/lokihub")
		logger.Logger.Info().Interface("workdir", appConfig.Workdir).Msg("No workdir specified, using default")
	}
	// make sure workdir exists
	if err := os.MkdirAll(appConfig.Workdir, os.ModePerm); err != nil {
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
		return nil, err
	}

	cfg, err := config.NewConfig(appConfig, gormDB)
	if err != nil {
		return nil, err
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

	eventPublisher.RegisterSubscriber(svc.transactionsService)
	eventPublisher.RegisterSubscriber(svc.nip47Service)

	eventPublisher.RegisterSubscriber(&paymentForwardedConsumer{
		db: gormDB,
	})

	svc.appStoreSvc = appstore.NewAppStoreService(cfg)
	svc.appStoreSvc.Start()

	eventPublisher.Publish(&events.Event{
		Event: "nwc_started",
		Properties: map[string]interface{}{
			"version": version.Tag,
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

// WarmCircleFollowingCache immediately fetches and caches providerPubkey's
// contact list, returning the fetched set. Called right after creating a
// following-policy Circle Hub, and reused by the manual "Sync" flow.
func (svc *service) WarmCircleFollowingCache(ctx context.Context, providerPubkey string) (map[string]struct{}, error) {
	return svc.socialCache.WarmFollowingCache(ctx, providerPubkey)
}

// WarmGeneralRelays re-connects the shared Nostr pool to the current General
// relay list — call after the user updates that setting.
func (svc *service) WarmGeneralRelays() {
	svc.socialCache.WarmGeneralRelays()
}

func (svc *service) ContactCount(ctx context.Context, ownerPubkey string) (int, error) {
	return svc.socialCache.ContactCount(ctx, ownerPubkey)
}

func (svc *service) PeekContactCount(ownerPubkey string) (int, bool) {
	return svc.socialCache.PeekContactCount(ownerPubkey)
}

func (svc *service) PeekContactSyncedAt(ownerPubkey string) (time.Time, bool) {
	return svc.socialCache.PeekContactSyncedAt(ownerPubkey)
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
