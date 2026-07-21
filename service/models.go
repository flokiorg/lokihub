package service

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/appstore"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
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
	GetStartupState() string
	ReloadNostr() error
	WarmCircleFollowingCache(ctx context.Context, providerPubkey string) (map[string]struct{}, error)
	// WarmGeneralRelays re-connects the shared Nostr pool to the current
	// General relay list — call after the user updates that setting so the
	// next kind:3 fetch (periodic refresh or manual Sync) doesn't have to
	// cold-dial a newly-added relay from scratch.
	WarmGeneralRelays()
	// ContactCount returns the number of pubkeys in ownerPubkey's cached (or
	// freshly-fetched-on-miss) kind:3 contact list.
	ContactCount(ctx context.Context, ownerPubkey string) (int, error)
	// PeekContactCount returns the cached contact count without ever fetching —
	// ok is false on a true cache miss. Safe to call from hot/list-style paths.
	PeekContactCount(ownerPubkey string) (count int, ok bool)
	// PeekContactSyncedAt returns when ownerPubkey's contact list was last
	// fetched from relays, without ever fetching — same peek-only contract as
	// PeekContactCount, used to report "last policy update" for following-policy
	// CircleIdentities.
	PeekContactSyncedAt(ownerPubkey string) (syncedAt time.Time, ok bool)
	GetAppStoreSvc() appstore.Service
	GetLiquidityManager() *manager.LiquidityManager
}
