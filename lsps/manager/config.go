package manager

import (
	"crypto/rand"
	"io"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/persist"
)

type ManagerConfig struct {
	LNClient      lnclient.LNClient
	KVStore       persist.KVStore
	EntropySource io.Reader
}

func NewManagerConfig(lnClient lnclient.LNClient, kvStore persist.KVStore) *ManagerConfig {
	return &ManagerConfig{
		LNClient:      lnClient,
		KVStore:       kvStore,
		EntropySource: rand.Reader,
	}
}
