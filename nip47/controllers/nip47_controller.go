package controllers

import (
	"context"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cash/cashwallet"
	"github.com/flokiorg/lokihub/cash/cashwithdraw"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/transactions"
	"github.com/flokiorg/lokihub/ttlcache"
	"gorm.io/gorm"
)

// NostrSocialCache checks whether a requester is authorized by the circle
// identity's policy. Defined here (not in service) to avoid the
// service→nip47→nip47/controllers import cycle.
type NostrSocialCache interface {
	IsAuthorized(ctx context.Context, requesterPubkey string, identity *db.CircleIdentity, gormDB *gorm.DB) (bool, error)
}

type nip47Controller struct {
	lnClient            lnclient.LNClient
	db                  *gorm.DB
	eventPublisher      events.EventPublisher
	permissionsService  permissions.PermissionsService
	transactionsService transactions.TransactionsService
	appsService         apps.AppsService
	keys                keys.Keys
	socialCache         NostrSocialCache
	cashRateLimiter     RateLimiter
	cashClaimLimiter    RateLimiter
	circleRateLimiter   RateLimiter
	iaChecker           cashwallet.IATrustChecker
	// privateIdentity returns the node identity get_info reports as
	// circle_wallet.identity, or "" when the private transport is not running.
	privateIdentity func() string
	// lineageLimiter bounds spin-offs per bill lineage; nil applies no limit.
	lineageLimiter RateLimiter
	// cashHooks is the cash commit's fault-injection seam; tests only.
	cashHooks *cashwallet.TestHooks
	// withdraw serves on-chain withdrawals; nil offers none.
	withdraw *cashwithdraw.Service
	// orderInfo and listedInfo hold the get_info results answered on public
	// connections: an Order connection's by client key, a listed circle
	// connection's by app id. Nil caches nothing.
	orderInfo  *ttlcache.Cache[string]
	listedInfo *ttlcache.Cache[uint]

	cfg config.Config
}

func NewNip47Controller(
	lnClient lnclient.LNClient,
	db *gorm.DB,
	eventPublisher events.EventPublisher,
	permissionsService permissions.PermissionsService,
	transactionsService transactions.TransactionsService,
	appsService apps.AppsService,
	keys keys.Keys,
	socialCache NostrSocialCache,
	cashRateLimiter RateLimiter,
	cashClaimLimiter RateLimiter,
	circleRateLimiter RateLimiter,
	cfg config.Config,
	iaChecker cashwallet.IATrustChecker) *nip47Controller {
	return &nip47Controller{
		lnClient:            lnClient,
		db:                  db,
		eventPublisher:      eventPublisher,
		permissionsService:  permissionsService,
		transactionsService: transactionsService,
		appsService:         appsService,
		keys:                keys,
		socialCache:         socialCache,
		cashRateLimiter:     cashRateLimiter,
		cashClaimLimiter:    cashClaimLimiter,
		circleRateLimiter:   circleRateLimiter,
		cfg:                 cfg,
		iaChecker:           iaChecker,
	}
}

// WithPrivateIdentity sets where get_info reads the Hub's private-transport identity from.
func (controller *nip47Controller) WithPrivateIdentity(privateIdentity func() string) *nip47Controller {
	controller.privateIdentity = privateIdentity
	return controller
}

// WithGetInfoCaches sets the caches get_info answers public connections from.
// They live on the service, so every per-request controller shares them, and the
// code that changes what get_info shows invalidates the same instances.
func (controller *nip47Controller) WithGetInfoCaches(orders *ttlcache.Cache[string], listed *ttlcache.Cache[uint]) *nip47Controller {
	controller.orderInfo = orders
	controller.listedInfo = listed
	return controller
}

// WithLineageLimiter sets the limiter that bounds spin-offs per bill lineage. It
// lives on the service, so every per-item controller shares one.
func (controller *nip47Controller) WithLineageLimiter(limiter RateLimiter) *nip47Controller {
	controller.lineageLimiter = limiter
	return controller
}
