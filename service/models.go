package service

import (
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/loki"
	"github.com/flokiorg/lokihub/pkg/appstore"
	"github.com/flokiorg/lokihub/service/keys"
	"github.com/flokiorg/lokihub/swaps"
	"github.com/flokiorg/lokihub/transactions"
)

type RelayStatus struct {
	Url    string
	Online bool
}

type Service interface {
	StartApp(encryptionKey string) error
	StopApp()
	Shutdown()

	// TODO: remove getters (currently used by http / wails services)
	GetLokiSvc() loki.LokiService

	GetEventPublisher() events.EventPublisher
	GetLNClient() lnclient.LNClient
	GetTransactionsService() transactions.TransactionsService
	GetSwapsService() swaps.SwapsService
	InitSwapsService()
	GetDB() *gorm.DB
	GetConfig() config.Config
	GetKeys() keys.Keys
	GetRelayStatuses() []RelayStatus
	GetStartupState() string
	ReloadNostr() error
	GetAppStoreSvc() appstore.Service
}
