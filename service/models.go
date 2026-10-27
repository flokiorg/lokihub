package service

import (
	"context"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/appstore"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/listing"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/loki"
	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/flokiorg/lokihub/swaps"
	"github.com/flokiorg/lokihub/transactions"
)

type RelayStatus struct {
	Url    string
	Online bool
}

type Service interface {
	StartApp(encryptionKey string) error
	StopApp(ctx context.Context)
	Shutdown(ctx context.Context)

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
	// GetPrivateTransportStatus returns the hub's current NIP-CASH
	// private-transport announcement status — node/inbox identity and
	// whether (and to how many relays) the announcement was published. The
	// zero value means the hub hasn't attempted to start it yet.
	GetPrivateTransportStatus() PrivateTransportStatus
	// PublishNodeProfile signs and publishes a kind-0 profile for the hub's
	// own node identity to the General relay pool. Returns an error if the
	// LN backend cannot sign (no TransportSigner) or no relay accepted it.
	PublishNodeProfile(ctx context.Context, metadata NodeProfileMetadata) error
	// PublishNodeNote signs and publishes a kind-1 text note for the hub's
	// own node identity, same relay pool and signing identity as
	// PublishNodeProfile. First version: plain text only.
	PublishNodeNote(ctx context.Context, content string) (*PublishedNodeEvent, error)
	GetStartupState() string
	ReloadNostr() error
	// WarmGeneralRelays re-connects the shared Nostr pool to the current
	// General relay list — call after the user updates that setting so the
	// next follow-set fetch (periodic refresh or manual Sync) doesn't have to
	// cold-dial a newly-added relay from scratch.
	WarmGeneralRelays()
	// RefreshFollowSet fetches the follow set at (provider, d) now, whatever
	// is cached. ErrCircleMembershipUnresolved means it wasn't found.
	RefreshFollowSet(ctx context.Context, provider, d string) (*FollowSet, error)
	// FollowSet returns the cached follow set, fetching it once on a miss.
	FollowSet(ctx context.Context, provider, d string) (*FollowSet, error)
	// PeekFollowSet returns the cached follow set without ever fetching. ok is
	// false on a miss. Safe to call from hot/list-style paths.
	PeekFollowSet(provider, d string) (*FollowSet, bool)
	// PublishFollowSet signs the follow set d as the node identity and
	// publishes it to the General relays.
	PublishFollowSet(ctx context.Context, d string, content FollowSetContent) (*FollowSet, error)
	// NodeNostrIdentity is the node identity in Nostr form (x-only hex), the
	// author of every follow set PublishFollowSet publishes.
	NodeNostrIdentity() (string, error)
	GetAppStoreSvc() appstore.Service
	GetLiquidityManager() *manager.LiquidityManager
	// GetListingService is the offer-listing service (WP-25), nil until started.
	GetListingService() *listing.Service
}
