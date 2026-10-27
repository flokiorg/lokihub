package controllers

import (
	"context"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/transactions"
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

// WithLineageLimiter sets the limiter that bounds spin-offs per bill lineage. It
// lives on the service, so every per-item controller shares one.
func (controller *nip47Controller) WithLineageLimiter(limiter RateLimiter) *nip47Controller {
	controller.lineageLimiter = limiter
	return controller
}
