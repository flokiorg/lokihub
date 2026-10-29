package nip47

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// A bill deleted while one of its items is being served (a drained bill's
// asynchronous auto-delete) is answered as a destroyed bill, its tombstone,
// rather than dropped on the request event's foreign key ("Failed to record a
// private item: FOREIGN KEY constraint failed", seen in live run18).
func TestServePrivateItem_TargetDeletedMidServeGetsTombstone(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	nip47svc := NewNip47Service(svc.DB, svc.Cfg, svc.Keys, svc.EventPublisher, nil)
	walletPubkey, recipientPriv, _, connPriv := privateDispatchFixture(t, svc)
	var bill db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", walletPubkey).First(&bill).Error)
	require.NoError(t, svc.DB.Model(&db.CashHubConfig{}).Where("app_id = ?", *bill.ParentAppID).
		Update("spent_retention_secs", 3600).Error)

	deleted := 0
	beforeRecordPrivateItem = func(app *db.App) {
		if app.ID != bill.ID || deleted > 0 {
			return
		}
		deleted++
		// What archiving a drained bill leaves: the archive row, the app gone.
		retained := time.Now().Add(time.Hour)
		require.NoError(t, svc.DB.Create(&db.CashBillArchive{WalletAppID: bill.ID, HubAppID: *bill.ParentAppID,
			WalletPubkey: walletPubkey, ConnectionPubkey: bill.AppPubkey, EndedAt: time.Now(), RetainedUntil: &retained,
			Outcome: db.CashBillOutcomeDrained, TotalMloki: 1000, FundedMloki: 1000}).Error)
		require.NoError(t, svc.DB.Delete(&db.App{}, bill.ID).Error)
	}
	t.Cleanup(func() { beforeRecordPrivateItem = nil })
	// The request-event insert never runs into the deleted app's foreign key.
	var insertErrs []error
	require.NoError(t, svc.DB.Callback().Create().After("gorm:create").Register("test:request_event_errors", func(tx *gorm.DB) {
		if tx.Statement.Table == "request_events" && tx.Error != nil {
			insertErrs = append(insertErrs, tx.Error)
		}
	}))
	t.Cleanup(func() { _ = svc.DB.Callback().Create().Remove("test:request_event_errors") })

	hubXOnly := strings.Repeat("ab", 32)
	nonce := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(time.Minute).Unix()
	item := buildItem(t, "s1", walletPubkey, constants.NIP47MethodCashStatus, `{}`,
		recipientPriv, connPriv, hubXOnly, nonce, notAfter)
	result, served := nip47svc.ServePrivateItem(context.TODO(), svc.LNClient, item,
		PrivateItemBinding{HubXOnly: hubXOnly, Nonce: nonce, NotAfter: notAfter})
	require.Equal(t, 1, deleted)
	require.True(t, served, "the holder of a bill destroyed mid-serve is answered")
	require.Nil(t, result.Error)
	status, ok := nipcash.ParseTombstone(result.Result)
	require.True(t, ok, "answered with the tombstone: %s", result.Result)
	assert.Equal(t, nipcash.StatusSpent, status.Status)
	assert.Empty(t, insertErrs, "no foreign-key failure on the request event")
}
