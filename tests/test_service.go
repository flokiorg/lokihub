package tests

import (
	"strconv"
	"testing"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/tests/db"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/lnclient"
)

func CreateTestService(t *testing.T) (svc *TestService, err error) {
	return CreateTestServiceWithMnemonic(t, "", "")
}

func CreateTestServiceWithMnemonic(t *testing.T, mnemonic string, unlockPassword string) (svc *TestService, err error) {
	logger.Init(strconv.Itoa(int(4)))

	gormDb, err := db.NewDB(t)
	if err != nil {
		return nil, err
	}

	mockLn, err := NewMockLn()
	if err != nil {
		return nil, err
	}

	appConfig := &config.AppConfig{
		Workdir: ".test",
		// Explicit, since this literal bypasses envconfig.Process's struct-tag
		// defaults (0 means "no limit" for these two fields, so leaving them
		// zero-value here would silently disable rate-limit tests).
		CashWalletRateLimitPerHour:      10,
		CashWalletClaimRateLimitPerHour: 20,
		CircleWalletRateLimitPerHour:    3,
	}

	cfg, err := config.NewConfig(
		appConfig,
		gormDb,
	)
	if err != nil {
		return nil, err
	}
	// Every test hub gets a relay, because a hub without one cannot mint: a bill is
	// reachable only through the relay hints embedded in its own token, so
	// cashwallet.Resolve refuses rather than hand back an unreachable credential.
	//
	// Before config.GetRelayUrls stopped returning []string{""} for an unset
	// config, these tests were silently minting bills carrying a single EMPTY
	// relay hint — the exact defect that guard exists to prevent. They passed only
	// because nothing dialed the result. Setting a real value here makes the whole
	// suite exercise the shape a working deployment actually produces.
	if err = cfg.SetUpdate("Relay", "wss://relay.test", ""); err != nil {
		return nil, err
	}

	keys := keys.NewKeys()

	if mnemonic != "" {
		if err = cfg.SetUpdate("Mnemonic", mnemonic, unlockPassword); err != nil {
			return nil, err
		}
	}

	if err = keys.Init(cfg, unlockPassword); err != nil {
		return nil, err
	}

	eventPublisher := events.NewEventPublisher()

	appsService := apps.NewAppsService(gormDb, eventPublisher, keys, cfg)

	return &TestService{
		Cfg:            cfg,
		LNClient:       mockLn,
		EventPublisher: eventPublisher,
		DB:             gormDb,
		Keys:           keys,
		AppsService:    appsService,
	}, nil
}

type TestService struct {
	Keys           keys.Keys
	Cfg            config.Config
	LNClient       lnclient.LNClient
	EventPublisher events.EventPublisher
	AppsService    apps.AppsService
	DB             *gorm.DB
}

func (s *TestService) Remove() {
	db.CloseDB(s.DB)
}
