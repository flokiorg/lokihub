package controllers

import (
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

func NewTestNip47Controller(svc *tests.TestService) *nip47Controller {
	permissionsSvc := permissions.NewPermissionsService(svc.DB, svc.EventPublisher)
	transactionsSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	return NewNip47Controller(svc.LNClient, svc.DB, svc.EventPublisher, permissionsSvc, transactionsSvc, svc.AppsService, svc.Cfg)
}
