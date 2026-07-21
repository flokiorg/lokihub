package controllers

import (
	"context"
	"sync"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/jitwallet"
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
	jitRateLimiter      RateLimiter
	jitClaimLimiter     RateLimiter
	circleRateLimiter   RateLimiter
	iaChecker           jitwallet.IATrustChecker

	// activeCircleInvoices guards per-app balance cap checks. An entry is held
	// from just before the balance read until MakeInvoice completes, preventing
	// two concurrent requests from both passing the cap check on the same stale balance.
	activeCircleInvoices sync.Map // map[uint]struct{}

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
	jitRateLimiter RateLimiter,
	jitClaimLimiter RateLimiter,
	circleRateLimiter RateLimiter,
	cfg config.Config,
	iaChecker jitwallet.IATrustChecker) *nip47Controller {
	return &nip47Controller{
		lnClient:            lnClient,
		db:                  db,
		eventPublisher:      eventPublisher,
		permissionsService:  permissionsService,
		transactionsService: transactionsService,
		appsService:         appsService,
		keys:                keys,
		socialCache:         socialCache,
		jitRateLimiter:      jitRateLimiter,
		jitClaimLimiter:     jitClaimLimiter,
		circleRateLimiter:   circleRateLimiter,
		cfg:                 cfg,
		iaChecker:           iaChecker,
	}
}
