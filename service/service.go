package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/adrg/xdg"
	"gorm.io/gorm"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"

	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/loki"
	"github.com/flokiorg/lokihub/pkg/appstore"
	"github.com/flokiorg/lokihub/pkg/version"
	"github.com/flokiorg/lokihub/service/keys"
	"github.com/flokiorg/lokihub/swaps"
	"github.com/flokiorg/lokihub/transactions"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/manager"
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
	wg                  *sync.WaitGroup
	nip47Service        nip47.Nip47Service
	liquidityManager    *manager.LiquidityManager
	appCancelFn         context.CancelFunc
	nostrCancelFn       context.CancelFunc
	keys                keys.Keys
	relayStatuses       []RelayStatus
	startupState        string
}

func NewService(ctx context.Context) (*service, error) {
	// Load config from environment variables / .GetEnv() file
	godotenv.Load(".env")
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
	os.MkdirAll(appConfig.Workdir, os.ModePerm)

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

	var wg sync.WaitGroup
	svc := &service{
		cfg:            cfg,
		ctx:            ctx,
		wg:             &wg,
		eventPublisher: eventPublisher,
		lokiSvc:        lokiSvc,

		nip47Service:        nip47.NewNip47Service(gormDB, cfg, keys, eventPublisher),
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
			svc.StartApp(autoUnlockPassword)
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
	logger.Logger.Info().Msgf("Received a notice %s", notice)
}

func finishRestoreNode(workDir string) error {
	restoreDir := filepath.Join(workDir, "restore")
	if restoreDirStat, err := os.Stat(restoreDir); err == nil && restoreDirStat.IsDir() {
		logger.Logger.Info().Str("restoreDir", restoreDir).Msgf("Restore directory found. Finishing Node restore")

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

func (svc *service) Shutdown() {
	svc.StopApp()
	svc.eventPublisher.PublishSync(&events.Event{
		Event: "nwc_stopped",
	})
	db.Stop(svc.db)
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
	logger.Logger.Debug().Msg("Cleaning up excess events")

	maxEvents := 1000
	// estimated less than 1 second to delete, it should not lock the DB
	maxEventsToDelete := 5000
	// if we only have a few excess events, don't run the task
	minEventsToDelete := 100

	var events []db.RequestEvent
	err := svc.db.Select("id").Order("id asc").Limit(maxEvents + maxEventsToDelete).Find(&events).Error
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to fetch request events")
	}

	numEventsToDelete := len(events) - maxEvents

	if numEventsToDelete < minEventsToDelete {
		return
	}
	deleteEventsBelowId := events[numEventsToDelete].ID

	logger.Logger.Debug().
		Int("amount", numEventsToDelete).
		Uint("below_id", deleteEventsBelowId).
		Msg("Removing excess events")

	startTime := time.Now()
	err = svc.db.Exec("delete from request_events where id < ?", deleteEventsBelowId).Error
	if err != nil {
		logger.Logger.Error().Err(err).
			Int("amount", numEventsToDelete).
			Uint("below_id", deleteEventsBelowId).
			Msg("Failed to delete excess request events")
		return
	}
	logger.Logger.Info().
		Int("amount", numEventsToDelete).
		Uint("below_id", deleteEventsBelowId).
		Float64("duration_seconds", time.Since(startTime).Seconds()).
		Msg("Removed excess events")

	// TODO: REMOVE AFTER 2026-01-01
	// this is needed due to cascading delete previously not working
	err = svc.db.Exec("delete from response_events where request_id < ?", deleteEventsBelowId).Error
	if err != nil {
		logger.Logger.Error().Err(err).
			Int("amount", numEventsToDelete).
			Uint("below_id", deleteEventsBelowId).
			Msg("Failed to delete excess response events")
		return
	}
}
