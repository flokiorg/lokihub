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
	// RetainedUntil is set here for the same reason archive.CashBill sets it in
	// production: the deadline is materialised on the row, so a fixture that
	// leaves it nil is a bill with no tombstone, not one whose window gets
	// derived on read. Retention 0 stays nil, which is what "no tombstone"
	// means.
	var retainedUntil *time.Time
	if retentionSecs > 0 {
		deadline := endedAt.Add(time.Duration(retentionSecs) * time.Second)
		retainedUntil = &deadline
	}
	require.NoError(t, gormDB.Create(&db.CashBillArchive{
		WalletAppID:   hub.ID + 900_000,
		HubAppID:      hub.ID,
		WalletPubkey:  pubkey,
		EndedAt:       endedAt,
		RetainedUntil: retainedUntil,
		Outcome:       db.CashBillOutcomeDrained,
		TotalMloki:    1000,
		FundedMloki:   1000,
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

// The retention deadline is inclusive, and this pins the instant.
//
// That exact moment is quoted to the caller as retained_until, so a request
// landing on it must still be answered — silence there would contradict the
// figure the hub itself published. Deterministic rather than clock-driven:
// the deadline is computed, then probed at one tick either side of it.
func TestSpentBillStillAnswerable_DeadlineIsInclusive(t *testing.T) {
	gormDB := retentionTestDB(t)
	pubkey := randomHex30(t)
	seedSpentBill(t, gormDB, pubkey, time.Now().Add(-time.Hour), 3600)

	deadline, ok := db.SpentBillRetainedUntil(gormDB, pubkey)
	require.True(t, ok, "the fixture must be inside a real retention policy")

	assert.True(t, db.SpentBillStillAnswerable(gormDB, pubkey, deadline.Add(-time.Nanosecond)),
		"a moment before the deadline is plainly inside the window")
	assert.True(t, db.SpentBillStillAnswerable(gormDB, pubkey, deadline),
		"the deadline itself is inside the window — it is the instant the hub promised")
	assert.False(t, db.SpentBillStillAnswerable(gormDB, pubkey, deadline.Add(time.Nanosecond)),
		"a moment after it, the hub returns to silence")
}

// TestRecomputeSpentRetention_LoweringShortensExistingBills is the behaviour the
// materialised column exists to preserve: retention is live policy, so lowering
// it must apply to bills the Hub has already destroyed — even though that
// shortens a window already quoted to a caller as retained_until.
func TestRecomputeSpentRetention_LoweringShortensExistingBills(t *testing.T) {
	gormDB := retentionTestDB(t)
	endedAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	pubkey := "a1" + randomHex30(t)
	seedSpentBill(t, gormDB, pubkey, endedAt, 15*24*60*60)

	before, ok := db.SpentBillRetainedUntil(gormDB, pubkey)
	require.True(t, ok)
	assert.Equal(t, endedAt.Add(15*24*time.Hour).Unix(), before.Unix())

	var bill db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_pubkey = ?", pubkey).First(&bill).Error)
	require.NoError(t, db.RecomputeSpentRetention(gormDB, bill.HubAppID, time.Hour, false))

	after, ok := db.SpentBillRetainedUntil(gormDB, pubkey)
	require.True(t, ok, "the bill still has a policy, just a shorter one")
	assert.Equal(t, endedAt.Add(time.Hour).Unix(), after.Unix(),
		"lowering retention must move an already-destroyed bill's deadline")

	// Two hours after the spend with a one-hour window: it is now past.
	assert.False(t, db.SpentBillStillAnswerable(gormDB, pubkey, time.Now()),
		"a window that has been lowered past the present must stop answering")
}

// TestRecomputeSpentRetention_RaisingRevivesExpiredBills is the other direction,
// and the one that needs the service layer to re-register the wallet: a bill
// whose window had passed becomes answerable again.
func TestRecomputeSpentRetention_RaisingRevivesExpiredBills(t *testing.T) {
	gormDB := retentionTestDB(t)
	endedAt := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	pubkey := "a2" + randomHex30(t)
	seedSpentBill(t, gormDB, pubkey, endedAt, 60)

	require.False(t, db.SpentBillStillAnswerable(gormDB, pubkey, time.Now()),
		"a one-minute window two hours ago has passed")

	var bill db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_pubkey = ?", pubkey).First(&bill).Error)
	require.NoError(t, db.RecomputeSpentRetention(gormDB, bill.HubAppID, 15*24*time.Hour, false))

	assert.True(t, db.SpentBillStillAnswerable(gormDB, pubkey, time.Now()),
		"raising retention must bring an expired bill back inside its window")
}

// TestRecomputeSpentRetention_ZeroClearsTheDeadline pins that disabling
// retention removes the tombstone outright rather than leaving a stale deadline.
func TestRecomputeSpentRetention_ZeroClearsTheDeadline(t *testing.T) {
	gormDB := retentionTestDB(t)
	endedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	pubkey := "a3" + randomHex30(t)
	seedSpentBill(t, gormDB, pubkey, endedAt, 3600)

	var bill db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_pubkey = ?", pubkey).First(&bill).Error)
	require.NoError(t, db.RecomputeSpentRetention(gormDB, bill.HubAppID, 0, false))

	var after db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_pubkey = ?", pubkey).First(&after).Error)
	assert.Nil(t, after.RetainedUntil, "no retention means no deadline on the row")

	_, ok := db.SpentBillRetainedUntil(gormDB, pubkey)
	assert.False(t, ok)
}

// TestRecomputeSpentRetention_OnlyMissingLeavesSetDeadlines is what the backfill
// migration relies on: it fills a new column without rewriting anything a live
// policy change has already set.
func TestRecomputeSpentRetention_OnlyMissingLeavesSetDeadlines(t *testing.T) {
	gormDB := retentionTestDB(t)
	endedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	pubkey := "a4" + randomHex30(t)
	seedSpentBill(t, gormDB, pubkey, endedAt, 3600)

	var bill db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_pubkey = ?", pubkey).First(&bill).Error)
	require.NotNil(t, bill.RetainedUntil)

	require.NoError(t, db.RecomputeSpentRetention(gormDB, bill.HubAppID, 99*time.Hour, true))

	after, ok := db.SpentBillRetainedUntil(gormDB, pubkey)
	require.True(t, ok)
	assert.Equal(t, endedAt.Add(time.Hour).Unix(), after.Unix(),
		"onlyMissing must not rewrite a deadline that is already set")
}
