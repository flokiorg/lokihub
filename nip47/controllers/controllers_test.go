package controllers

import (
	"context"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

func NewTestNip47Controller(svc *tests.TestService) *nip47Controller {
	permissionsSvc := permissions.NewPermissionsService(svc.DB, svc.EventPublisher)
	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	iaManager := apps.NewIdentityAuthorityManager(svc.DB)
	return NewNip47Controller(svc.LNClient, svc.DB, svc.EventPublisher, permissionsSvc, transactionsSvc, svc.AppsService, svc.Keys, nil, NewRateLimiter(), NewRateLimiter(), NewRateLimiter(), svc.Cfg, iaManager)
}

// mockSocialCache is a controllable NostrSocialCache for unit tests.
type mockSocialCache struct {
	authorized bool
	err        error
}

func (m *mockSocialCache) IsAuthorized(_ context.Context, _ string, _ *db.CircleIdentity, _ *gorm.DB) (bool, error) {
	return m.authorized, m.err
}

func NewTestNip47ControllerWithSocialCache(svc *tests.TestService, socialCache NostrSocialCache) *nip47Controller {
	permissionsSvc := permissions.NewPermissionsService(svc.DB, svc.EventPublisher)
	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	iaManager := apps.NewIdentityAuthorityManager(svc.DB)
	return NewNip47Controller(svc.LNClient, svc.DB, svc.EventPublisher, permissionsSvc, transactionsSvc, svc.AppsService, svc.Keys, socialCache, NewRateLimiter(), NewRateLimiter(), NewRateLimiter(), svc.Cfg, iaManager)
}
