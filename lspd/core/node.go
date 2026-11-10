package core

import (
	ecies "github.com/flokiorg/lokihub/internal/eciesgo"
	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"golang.org/x/sync/singleflight"
)

type Node struct {
	NodeId []byte
	Client lightning.Client
	// NodeKeySigner signs NIP-LSP announcements with the node key (T8-1);
	// the flnd client. Only services.NewAnnouncementSigner uses it.
	NodeKeySigner lightning.NodeKeySigner
	// Chain and Network are the node's, from flnd GetInfo at start-up
	// (the NIP-LSP n and network tags).
	Chain   string
	Network string
	// NodeConfig is held behind an atomic ref (not a raw pointer) so
	// concurrent readers (e.g. the interceptor's HTLC hot path) and a
	// concurrent writer (the dashboard's config-update handler) can't
	// race on the struct's fields — see config.NodeConfigRef and C-3.
	NodeConfig          *config.NodeConfigRef
	PrivateKey          *btcec.PrivateKey
	PublicKey           *btcec.PublicKey
	EciesPrivateKey     *ecies.PrivateKey
	EciesPublicKey      *ecies.PublicKey
	OpenChannelReqGroup singleflight.Group
	Tokens              []string
}
