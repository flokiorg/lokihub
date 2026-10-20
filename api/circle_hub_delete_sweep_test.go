package api

// Covers DeleteCircleHub's mode=all: every wallet's balance goes back to the
// hub, the hub's balance to the node's main balance, and only then is the hub
// deleted, so no money disappears unrecorded.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// newPayingCircleChild is a circle wallet that can pay, so a reclaim can move
// its balance back to the hub.
func newPayingCircleChild(t *testing.T, svc *tests.TestService, hub *db.App, name string) *db.App {
	t.Helper()
	child, _, err := svc.AppsService.CreateApp(
		name, "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.PAY_INVOICE_SCOPE}, db.AppKindCircleWallet, &hub.ID, db.ParentKindCircle, nil,
	)
	require.NoError(t, err)
	return child
}

// mainBalanceReceivedMloki sums what has settled into the node's main balance
// (incoming payments assigned to no app).
func mainBalanceReceivedMloki(t *testing.T, svc *tests.TestService) int64 {
	t.Helper()
	var total int64
	require.NoError(t, svc.DB.Model(&db.Transaction{}).
		Where("app_id IS NULL AND type = ? AND state = ?", constants.TRANSACTION_TYPE_INCOMING, constants.TRANSACTION_STATE_SETTLED).
		Select("COALESCE(SUM(amount_mloki), 0)").Scan(&total).Error)
	return total
}

// secondSelfInvoice is a second 123,000 mloki invoice to the same payee as
// tests.MockInvoice, so two internal transfers in one test don't share a
// payment hash.
const (
	secondSelfInvoice     = "lntbs1230n1pnkq7q2dqqnp4q09a0z84kg4a2m38zjllw43h953fx5zvqe8qxfgw694ymkq26u8zcpp54sde879ktfrwnt4re3t2ckkrt5tr6dgv6cfdjgkar7942ruccvuqsp52qlk3rxr926s630fmnc5mg6sexnng4cyyfas4msrms8j6q28j8ys9qyysgqcqpcxq8zals8sqgjd3a60n6dy92jn7ggtkywhw952sc302qj0cwfupp7gayadznaj5cahvuq7py8p7hnq8yxylru6279urzxta3783cxze2atj9zmwadcq36muep"
	secondSelfPaymentHash = "ac1b93f8b65a46e9aea3cc56ac5ac35d163d350cd612d922dd1f8b550f98c338"
)

func TestDeleteCircleHub_ModeAll_SweepsWalletThroughHubToMainBalance(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = selfPaymentPubkey
	// The wallet's reclaim to the hub, then the hub's balance to main.
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-a", Amount: 123_000},
		{Type: "incoming", Invoice: secondSelfInvoice, PaymentHash: secondSelfPaymentHash, Preimage: "preimage-b", Amount: 123_000},
	}

	hub := newCircleHub(t, svc, 300_000, 3600)
	child := newPayingCircleChild(t, svc, hub, "funded-member")
	tests.FundApp(svc, child.ID, 123_000, "sweep-fund-child")
	before := mainBalanceReceivedMloki(t, svc)

	result, err := newTestAPIWithService(t, svc).DeleteCircleHub(hub, db.CircleDeleteModeAll)
	require.NoError(t, err)
	assert.True(t, result.HubDeleted)
	assert.Equal(t, []uint{child.ID}, result.DeletedChildIDs)
	assert.Empty(t, result.SkippedChildIDs)

	assert.Equal(t, int64(123_000), mainBalanceReceivedMloki(t, svc)-before,
		"the wallet's balance reaches the main balance as a recorded transfer")
	assert.Nil(t, svc.AppsService.GetAppById(hub.ID))
	assert.Nil(t, svc.AppsService.GetAppById(child.ID))
}

func TestDeleteCircleHub_ModeAll_SweepsHubBalanceToMainBalance(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = selfPaymentPubkey
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-a", Amount: 123_000},
	}
	hub := newCircleHub(t, svc, 300_000, 3600)
	tests.FundApp(svc, hub.ID, 123_000, "sweep-fund-hub")
	before := mainBalanceReceivedMloki(t, svc)

	result, err := newTestAPIWithService(t, svc).DeleteCircleHub(hub, db.CircleDeleteModeAll)
	require.NoError(t, err)
	assert.True(t, result.HubDeleted)
	assert.Equal(t, int64(123_000), mainBalanceReceivedMloki(t, svc)-before)
	assert.Nil(t, svc.AppsService.GetAppById(hub.ID))
}

// pendingPayments are the in-flight states that block deleting the app they
// belong to: its row must survive until the payment ends.
func pendingPayments() map[string]db.Transaction {
	future := time.Now().Add(time.Hour)
	return map[string]db.Transaction{
		"live invoice": {Type: constants.TRANSACTION_TYPE_INCOMING, State: constants.TRANSACTION_STATE_PENDING,
			AmountMloki: 50_000, PaymentHash: "sweep-in", ExpiresAt: &future},
		"held payment": {Type: constants.TRANSACTION_TYPE_INCOMING, State: constants.TRANSACTION_STATE_ACCEPTED,
			AmountMloki: 50_000, PaymentHash: "sweep-held"},
		"payment going out": {Type: constants.TRANSACTION_TYPE_OUTGOING, State: constants.TRANSACTION_STATE_PENDING,
			AmountMloki: 0, PaymentHash: "sweep-out"},
	}
}

func TestDeleteCircleHub_ModeAll_WalletPaymentInFlight_KeepsHubAndWallet(t *testing.T) {
	for name, pending := range pendingPayments() {
		t.Run(name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			hub := newCircleHub(t, svc, 300_000, 3600)
			busy := newPayingCircleChild(t, svc, hub, "busy-member")
			pending.AppId = &busy.ID
			require.NoError(t, svc.DB.Create(&pending).Error)

			result, err := newTestAPIWithService(t, svc).DeleteCircleHub(hub, db.CircleDeleteModeAll)
			require.NoError(t, err)
			assert.False(t, result.HubDeleted)
			assert.Equal(t, []uint{busy.ID}, result.SkippedChildIDs)
			assert.NotNil(t, svc.AppsService.GetAppById(hub.ID))
			assert.NotNil(t, svc.AppsService.GetAppById(busy.ID))
		})
	}
}

func TestDeleteCircleHub_ModeAll_RetryAfterPaymentEnds_Completes(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := newCircleHub(t, svc, 300_000, 3600)
	busy := newPayingCircleChild(t, svc, hub, "busy-member")
	pending := pendingPayments()["live invoice"]
	pending.AppId = &busy.ID
	require.NoError(t, svc.DB.Create(&pending).Error)

	theAPI := newTestAPIWithService(t, svc)
	result, err := theAPI.DeleteCircleHub(hub, db.CircleDeleteModeAll)
	require.NoError(t, err)
	require.False(t, result.HubDeleted)

	require.NoError(t, svc.DB.Model(&db.Transaction{}).Where("id = ?", pending.ID).
		Update("state", constants.TRANSACTION_STATE_FAILED).Error)

	result, err = theAPI.DeleteCircleHub(hub, db.CircleDeleteModeAll)
	require.NoError(t, err)
	assert.True(t, result.HubDeleted)
	assert.Equal(t, []uint{busy.ID}, result.DeletedChildIDs)
	assert.Nil(t, svc.AppsService.GetAppById(hub.ID))
}

func TestDeleteCircleHub_ModeAll_HubPaymentInFlight_KeepsHub(t *testing.T) {
	for name, pending := range pendingPayments() {
		t.Run(name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			hub := newCircleHub(t, svc, 300_000, 3600)
			pending.AppId = &hub.ID
			require.NoError(t, svc.DB.Create(&pending).Error)

			_, err = newTestAPIWithService(t, svc).DeleteCircleHub(hub, db.CircleDeleteModeAll)
			require.ErrorIs(t, err, constants.ErrInvalidParams)
			assert.NotNil(t, svc.AppsService.GetAppById(hub.ID))
		})
	}
}
