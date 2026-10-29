package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlemoney"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// U-15.12: a Circle Wallet's get_budget reports both caps for the current
// period (NIP-CW §What a Circle Wallet Connection Allows), each used figure
// clamped so the remaining room is never below zero, and renews_at at the
// period's end.
func TestGetBudget_CircleWallet_BothCapsClamped(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := &db.App{Name: "hub", AppPubkey: tests.RandomHex32(), Kind: db.AppKindCircleHub}
	require.NoError(t, svc.DB.Create(hub).Error)
	_, err = tests.AddCircleHubConfig(svc, hub.ID)
	require.NoError(t, err)
	wallet, _, err := svc.AppsService.CreateApp("member", "", 100, constants.BUDGET_RENEWAL_MONTHLY, nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &hub.ID, db.ParentKindCircle, nil)
	require.NoError(t, err)
	require.NoError(t, tests.SetCircleMaxReceive(svc, wallet.ID, 50_000))
	anchor := time.Now().Add(-time.Hour)
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Update("circle_budget_anchor_at", anchor.Unix()).Error)
	require.NoError(t, svc.DB.First(wallet, wallet.ID).Error)

	start := anchor.Unix()
	require.NoError(t, svc.DB.Create(&db.Transaction{AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_OUTGOING,
		State: constants.TRANSACTION_STATE_SETTLED, AmountMloki: 120_000, PaymentHash: tests.RandomHex32(),
		PaymentKind: circlemoney.KindLightning, CountsSend: true, PeriodStart: start}).Error)
	require.NoError(t, svc.DB.Create(&db.Transaction{AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_INCOMING,
		State: constants.TRANSACTION_STATE_SETTLED, AmountMloki: 30_000, PaymentHash: tests.RandomHex32(),
		PaymentKind: circlemoney.KindNode, PeriodStart: start}).Error)

	var resp *models.Response
	NewTestNip47Controller(svc).HandleGetBudgetEvent(context.TODO(), &models.Request{Method: "get_budget"}, 0, wallet,
		func(r *models.Response, _ nostr.Tags) { resp = r })
	require.NotNil(t, resp)
	require.Nil(t, resp.Error)
	budget := resp.Result.(*getBudgetResponse)
	assert.Equal(t, uint64(100_000), budget.TotalBudget)
	assert.Equal(t, uint64(100_000), budget.UsedBudget, "clamped to the cap")
	assert.Equal(t, constants.BUDGET_RENEWAL_MONTHLY, budget.RenewalPeriod)
	require.NotNil(t, budget.RenewsAt)
	next, err := nipcw.BudgetPeriodStart(anchor, constants.BUDGET_RENEWAL_MONTHLY, 1)
	require.NoError(t, err)
	assert.EqualValues(t, next.Unix(), *budget.RenewsAt)
	require.NotNil(t, budget.TotalReceiveBudget)
	assert.Equal(t, uint64(50_000), *budget.TotalReceiveBudget)
	assert.Equal(t, uint64(30_000), *budget.UsedReceiveBudget)
}
