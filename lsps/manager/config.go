package manager

import (
	"crypto/rand"
	"io"

	"github.com/flokiorg/lokihub/lnclient"
)

type ManagerConfig struct {
	LNClient         lnclient.LNClient
	LSPManager       *LSPManager
	EntropySource    io.Reader
	GetWebhookConfig func() (string, string)
}

func NewManagerConfig(lnClient lnclient.LNClient, lspManager *LSPManager) *ManagerConfig {
	return &ManagerConfig{
		LNClient:      lnClient,
		LSPManager:    lspManager,
		EntropySource: rand.Reader,
	}
}
