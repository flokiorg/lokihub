package manager

import (
	"crypto/rand"
	"io"

	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
)

type ManagerConfig struct {
	LNClient         lnclient.LNClient
	LSPManager       *LSPManager
	EventPublisher   events.EventPublisher
	EntropySource    io.Reader
	GetWebhookConfig func() (string, string)
	AppConfig        config.Config // Inject main config for centralized caching
}

func NewManagerConfig(lnClient lnclient.LNClient, lspManager *LSPManager, eventPublisher events.EventPublisher, appConfig config.Config) *ManagerConfig {
	return &ManagerConfig{
		LNClient:       lnClient,
		LSPManager:     lspManager,
		EventPublisher: eventPublisher,
		EntropySource:  rand.Reader,
		AppConfig:      appConfig,
	}
}
