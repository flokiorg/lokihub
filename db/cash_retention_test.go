package db_test

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// seedSpentBill writes the archive row + hub config the retention policy reads.
// retentionSecs of 0 means the tombstone is disabled for that hub.
func seedSpentBill(t *testing.T, gormDB *gorm.DB, pubkey string, endedAt time.Time, retentionSecs int) {
	t.Helper()
	hub := db.App{Name: "hub-" + pubkey[:6], Kind: db.AppKindCashHub}
	require.NoError(t, gormDB.Create(&hub).Error)
	require.NoError(t, gormDB.Create(&db.CashHubConfig{
		AppID:              hub.ID,
		PerWalletMaxMloki:  10_000,
		SpentRetentionSecs: retentionSecs,
	}).Error)
	require.NoError(t, gormDB.Create(&db.CashBillArchive{
		WalletAppID:  hub.ID + 900_000,
		HubAppID:     hub.ID,
		WalletPubkey: pubkey,
		EndedAt:      endedAt,
		Outcome:      db.CashBillOutcomeDrained,
		TotalMloki:   1000,
		FundedMloki:  1000,
	}).Error)
}

func randomHex30(t *testing.T) string {
	t.Helper()
	b := make([]byte, 15)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func retentionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)
	return svc.DB
}

// The window is measured from the spend, so a bill destroyed inside it is still
// answerable and one destroyed before it is not.
func TestSpentBillRetention_InsideAndPastWindow(t *testing.T) {
	gormDB := retentionTestDB(t)
	now := time.Now()

	seedSpentBill(t, gormDB, "aa"+randomHex30(t), now.Add(-time.Hour), 24*60*60)
	seedSpentBill(t, gormDB, "bb"+randomHex30(t), now.Add(-48*time.Hour), 24*60*60)

	var inside, past []db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_pubkey LIKE ?", "aa%").Find(&inside).Error)
	require.NoError(t, gormDB.Where("wallet_pubkey LIKE ?", "bb%").Find(&past).Error)
	require.Len(t, inside, 1)
	require.Len(t, past, 1)

	assert.True(t, db.SpentBillStillAnswerable(gormDB, inside[0].WalletPubkey, now),
		"a bill destroyed an hour ago is inside a 24h window")
	assert.False(t, db.SpentBillStillAnswerable(gormDB, past[0].WalletPubkey, now),
		"a bill destroyed two days ago is past a 24h window — back to silence")
}

// retained_until is exactly EndedAt + retention. It is quoted to the caller, so
// it must not be quietly padded.
func TestSpentBillRetention_DeadlineIsExact(t *testing.T) {
	gormDB := retentionTestDB(t)
	endedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	pubkey := "cc" + randomHex30(t)
	seedSpentBill(t, gormDB, pubkey, endedAt, 3600)

	deadline, ok := db.SpentBillRetainedUntil(gormDB, pubkey)
	require.True(t, ok)
	assert.Equal(t, endedAt.Add(time.Hour).Unix(), deadline.Unix(),
		"no hidden floor or padding — this instant is what the caller is told")
}

// 0 disables the tombstone: the hub falls silent the moment a bill is gone,
// which is the behaviour that predates the feature.
func TestSpentBillRetention_ZeroDisables(t *testing.T) {
	gormDB := retentionTestDB(t)
	now := time.Now()
	pubkey := "dd" + randomHex30(t)
	seedSpentBill(t, gormDB, pubkey, now, 0)

	_, ok := db.SpentBillRetainedUntil(gormDB, pubkey)
	assert.False(t, ok, "retention 0 means no tombstone at all")
	assert.False(t, db.SpentBillStillAnswerable(gormDB, pubkey, now))
}

// A pubkey this hub never served must never produce a deadline — that is the
// privacy invariant the whole design rests on.
func TestSpentBillRetention_UnknownPubkeyNeverAnswerable(t *testing.T) {
	gormDB := retentionTestDB(t)
	now := time.Now()

	_, ok := db.SpentBillRetainedUntil(gormDB, "ee"+randomHex30(t))
	assert.False(t, ok)
	assert.False(t, db.SpentBillStillAnswerable(gormDB, "ee"+randomHex30(t), now))
	assert.False(t, db.SpentBillStillAnswerable(gormDB, "", now), "empty pubkey")
}

// A bill whose hub has itself been deleted has no policy left to honour, so it
// falls back to silence rather than to some default.
func TestSpentBillRetention_HubGoneFallsBackToSilence(t *testing.T) {
	gormDB := retentionTestDB(t)
	now := time.Now()
	pubkey := "ff" + randomHex30(t)
	seedSpentBill(t, gormDB, pubkey, now, 24*60*60)

	var bill db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_pubkey = ?", pubkey).First(&bill).Error)
	require.NoError(t, gormDB.Where("app_id = ?", bill.HubAppID).Delete(&db.CashHubConfig{}).Error)

	_, ok := db.SpentBillRetainedUntil(gormDB, pubkey)
	assert.False(t, ok, "no hub config means no retention policy, not a default one")
}
