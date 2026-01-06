package controllers

import (
	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/transactions"
	"gorm.io/gorm"
)

type nip47Controller struct {
	lnClient            lnclient.LNClient
	db                  *gorm.DB
	eventPublisher      events.EventPublisher
	permissionsService  permissions.PermissionsService
	transactionsService transactions.TransactionsService
	appsService         apps.AppsService

	cfg config.Config
}

func NewNip47Controller(
	lnClient lnclient.LNClient,
	db *gorm.DB,
	eventPublisher events.EventPublisher,
	permissionsService permissions.PermissionsService,
	transactionsService transactions.TransactionsService,
	appsService apps.AppsService,
	cfg config.Config) *nip47Controller {
	return &nip47Controller{
		lnClient:            lnClient,
		db:                  db,
		eventPublisher:      eventPublisher,
		permissionsService:  permissionsService,
		transactionsService: transactionsService,
		appsService:         appsService,
		cfg:                 cfg,
	}
}
