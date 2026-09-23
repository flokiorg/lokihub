package archive

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/migrations"
	"github.com/flokiorg/lokihub/db/queries"
)

// These tests deliberately build their fixtures with plain inserts instead of
// the tests.CreateTestService helpers. db/queries is a leaf that the apps
// package imports, and tests imports apps — so reaching for those helpers here
// would close an import cycle. Direct inserts are also a better fit: what is
// under test is SQL behaviour, not service wiring.
func newArchiveTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	uri := filepath.Join(t.TempDir(), "archive_cash_bill_test.db")
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: uri})
	require.NoError(t, err)
	require.NoError(t, migrations.Migrate(gormDB))
	t.Cleanup(func() { _ = db.Stop(gormDB) })
	return gormDB
}

func randomHex32(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func newHub(t *testing.T, gormDB *gorm.DB) *db.App {
	t.Helper()
	hub := db.App{Name: "hub-" + randomHex32(t)[:8], Kind: db.AppKindCashHub}
	require.NoError(t, gormDB.Create(&hub).Error)
	return &hub
}

// newCashBill creates a cash_wallet child of hub, funds it with a settled
// incoming transaction, and attaches the given claims.
func newCashBill(t *testing.T, gormDB *gorm.DB, hub *db.App, fundedMloki int64, claims []db.CashWalletClaim) *db.App {
	t.Helper()
	pubkey := randomHex32(t)
	bill := db.App{
		Name:         "bill-" + pubkey[:8],
		Kind:         db.AppKindCashWallet,
		ParentAppID:  &hub.ID,
		ParentKind:   db.ParentKindCash,
		WalletPubkey: &pubkey,
	}
	require.NoError(t, gormDB.Create(&bill).Error)

	if fundedMloki > 0 {
		funding := db.Transaction{
			AppId:       &bill.ID,
			Type:        constants.TRANSACTION_TYPE_INCOMING,
			State:       constants.TRANSACTION_STATE_SETTLED,
			AmountMloki: uint64(fundedMloki),
			PaymentHash: randomHex32(t),
		}
		require.NoError(t, gormDB.Create(&funding).Error)
	}
	for i := range claims {
		claims[i].WalletAppID = bill.ID
		if claims[i].IdentityType == "" {
			claims[i].IdentityType = db.CashIdentityPubkey
		}
		if claims[i].IdentityValue == "" {
			claims[i].IdentityValue = randomHex32(t)
		}
		require.NoError(t, gormDB.Create(&claims[i]).Error)
	}
	return &bill
}

func archivedSlices(t *testing.T, gormDB *gorm.DB, walletAppID uint) []db.CashBillSliceArchive {
	t.Helper()
	var rows []db.CashBillSliceArchive
	require.NoError(t, gormDB.Where("wallet_app_id = ?", walletAppID).Order("id").Find(&rows).Error)
	return rows
}

func countRows(t *testing.T, gormDB *gorm.DB, model interface{}, query string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	require.NoError(t, gormDB.Model(model).Where(query, args...).Count(&n).Error)
	return n
}

// TestArchiveAndDeleteCashBillTx_RoundTrip is the basic contract: the bill's
// history survives, the app row does not.
func TestArchiveAndDeleteCashBillTx_RoundTrip(t *testing.T) {
	gormDB := newArchiveTestDB(t)
	hub := newHub(t, gormDB)

	claimed := time.Now().Add(-time.Minute)
	bill := newCashBill(t, gormDB, hub, 5000, []db.CashWalletClaim{
		{AmountMloki: 3000, ClaimedAt: &claimed},
		{AmountMloki: 2000},
	})

	now := time.Now()
	require.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
		return ArchiveAndDeleteCashBillTx(tx, bill, db.CashBillOutcomeExpired, 2000, now)
	}))

	assert.Zero(t, countRows(t, gormDB, &db.App{}, "id = ?", bill.ID),
		"the app row must be gone — silence depends on it")

	var archived db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_app_id = ?", bill.ID).First(&archived).Error)
	assert.Equal(t, hub.ID, archived.HubAppID)
	assert.Equal(t, *bill.WalletPubkey, archived.WalletPubkey)
	assert.Equal(t, db.CashBillOutcomeExpired, archived.Outcome)
	assert.EqualValues(t, 5000, archived.TotalMloki, "sum of the archived slices")
	assert.EqualValues(t, 5000, archived.FundedMloki, "settled incoming, read before the cascade")
	assert.EqualValues(t, 2000, archived.ReclaimedMloki)

	assert.Len(t, archivedSlices(t, gormDB, bill.ID), 2)

	// The claims cascaded away with the app; the archive is the only copy left.
	assert.Zero(t, countRows(t, gormDB, &db.CashWalletClaim{}, "wallet_app_id = ?", bill.ID))
}

// TestArchiveAndDeleteCashBillTx_Atomicity is the single most important test
// here: archive and delete must be all-or-nothing. A half-applied archive means
// either a ghost bill that still answers requests, or a bill destroyed with no
// record — the two failures this design exists to prevent.
func TestArchiveAndDeleteCashBillTx_Atomicity(t *testing.T) {
	gormDB := newArchiveTestDB(t)
	hub := newHub(t, gormDB)
	bill := newCashBill(t, gormDB, hub, 0, []db.CashWalletClaim{{AmountMloki: 1000}})

	err := gormDB.Transaction(func(tx *gorm.DB) error {
		if err := ArchiveAndDeleteCashBillTx(tx, bill, db.CashBillOutcomeDeleted, 0, time.Now()); err != nil {
			return err
		}
		// Fail AFTER a fully successful archive+delete, the way a caller
		// wrapping this in a larger transaction would on its own later step.
		return assertErr("caller rolled back")
	})
	require.Error(t, err)

	assert.EqualValues(t, 1, countRows(t, gormDB, &db.App{}, "id = ?", bill.ID),
		"the bill must survive a rolled-back archive")
	assert.Empty(t, archivedSlices(t, gormDB, bill.ID), "no slice rows may outlive the rollback")
	assert.Zero(t, countRows(t, gormDB, &db.CashBillArchive{}, "wallet_app_id = ?", bill.ID))
	assert.EqualValues(t, 1, countRows(t, gormDB, &db.CashWalletClaim{}, "wallet_app_id = ?", bill.ID),
		"the claim must come back with the bill")
}

// TestArchiveAndDeleteCashBillTx_MixedOutcomes pins per-slice derivation. One
// bill's slices can end differently, so copying the bill's outcome down would
// be wrong — and "drained" is not even a member of the slice vocabulary.
func TestArchiveAndDeleteCashBillTx_MixedOutcomes(t *testing.T) {
	gormDB := newArchiveTestDB(t)
	hub := newHub(t, gormDB)

	claimed := time.Now().Add(-time.Minute)
	spunOff := uint(4242)
	bill := newCashBill(t, gormDB, hub, 0, []db.CashWalletClaim{
		{AmountMloki: 1000, ClaimedAt: &claimed},
		{AmountMloki: 2000, ClaimedAt: &claimed, SpunOffToWalletAppID: &spunOff},
		{AmountMloki: 3000},
	})

	require.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
		return ArchiveAndDeleteCashBillTx(tx, bill, db.CashBillOutcomeExpired, 3000, time.Now())
	}))

	got := map[int64]string{}
	for _, s := range archivedSlices(t, gormDB, bill.ID) {
		got[s.AmountMloki] = s.Outcome
		assert.NotEqual(t, db.CashBillOutcomeDrained, s.Outcome,
			"a bill-level outcome must never leak into a slice row")
	}
	assert.Equal(t, map[int64]string{
		1000: db.CashSliceStatusRedeemed, // claimed, no spin-off target
		2000: db.CashSliceStatusSplit,    // claimed, value moved to another bill
		3000: db.CashSliceStatusExpired,  // unclaimed, and the bill expired
	}, got)
}

// TestArchiveAndDeleteCashBillTx_Idempotent: a retried delete must not
// double-archive. WalletAppID's unique index is what guarantees it, and app ids
// are never reused so it is safe as a natural key.
func TestArchiveAndDeleteCashBillTx_Idempotent(t *testing.T) {
	gormDB := newArchiveTestDB(t)
	hub := newHub(t, gormDB)
	bill := newCashBill(t, gormDB, hub, 0, []db.CashWalletClaim{{AmountMloki: 1000}})

	for i := 0; i < 2; i++ {
		require.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
			return ArchiveAndDeleteCashBillTx(tx, bill, db.CashBillOutcomeDeleted, 0, time.Now())
		}), "call %d", i+1)
	}

	assert.EqualValues(t, 1, countRows(t, gormDB, &db.CashBillArchive{}, "wallet_app_id = ?", bill.ID),
		"the second call finds no app row and must be a no-op")
	assert.Len(t, archivedSlices(t, gormDB, bill.ID), 1)
}

// TestArchiveAndDeleteCashBillTx_TotalIncludesEarlierArchivedSlice: an operator
// removing one recipient archives that slice while the bill lives on. The
// bill's own total must still account for it, or the archive would understate
// what the bill was worth.
func TestArchiveAndDeleteCashBillTx_TotalIncludesEarlierArchivedSlice(t *testing.T) {
	gormDB := newArchiveTestDB(t)
	hub := newHub(t, gormDB)
	bill := newCashBill(t, gormDB, hub, 0, []db.CashWalletClaim{
		{AmountMloki: 1000},
		{AmountMloki: 4000},
	})

	var claims []db.CashWalletClaim
	require.NoError(t, gormDB.Where("wallet_app_id = ?", bill.ID).Order("id").Find(&claims).Error)
	require.Len(t, claims, 2)

	// Remove one recipient the way DeleteCashClaim does: archive the slice,
	// then delete the claim row, while the bill stays alive.
	require.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
		if err := ArchiveCashSliceTx(tx, bill, claims[0], db.CashSliceStatusReclaimed, time.Now()); err != nil {
			return err
		}
		return tx.Delete(&db.CashWalletClaim{}, claims[0].ID).Error
	}))
	require.EqualValues(t, 1, countRows(t, gormDB, &db.App{}, "id = ?", bill.ID),
		"the bill itself stays alive")

	require.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
		return ArchiveAndDeleteCashBillTx(tx, bill, db.CashBillOutcomeDeleted, 0, time.Now())
	}))

	var archived db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_app_id = ?", bill.ID).First(&archived).Error)
	assert.EqualValues(t, 5000, archived.TotalMloki,
		"the earlier-archived slice must still count toward the bill's total")
	assert.Len(t, archivedSlices(t, gormDB, bill.ID), 2)
}

// TestArchiveAndDeleteDrainedCashBillTx_Guards: the drain guard is evaluated
// inside the transaction, and refuses anything that still backs live value.
func TestArchiveAndDeleteDrainedCashBillTx_Guards(t *testing.T) {
	gormDB := newArchiveTestDB(t)
	hub := newHub(t, gormDB)
	claimed := time.Now().Add(-time.Minute)

	t.Run("refuses while a sibling slice is unclaimed", func(t *testing.T) {
		bill := newCashBill(t, gormDB, hub, 0, []db.CashWalletClaim{
			{AmountMloki: 1000, ClaimedAt: &claimed},
			{AmountMloki: 2000},
		})
		err := gormDB.Transaction(func(tx *gorm.DB) error {
			return ArchiveAndDeleteDrainedCashBillTx(tx, bill, DrainGuards{IsolatedBalance: queries.GetIsolatedBalance, HasPendingIncoming: queries.HasPendingIncoming}, time.Now())
		})
		require.ErrorIs(t, err, ErrBillNotDrained)
		assert.EqualValues(t, 1, countRows(t, gormDB, &db.App{}, "id = ?", bill.ID))
		assert.Empty(t, archivedSlices(t, gormDB, bill.ID))
	})

	t.Run("refuses while the balance is nonzero", func(t *testing.T) {
		bill := newCashBill(t, gormDB, hub, 7000, []db.CashWalletClaim{
			{AmountMloki: 1000, ClaimedAt: &claimed},
		})
		err := gormDB.Transaction(func(tx *gorm.DB) error {
			return ArchiveAndDeleteDrainedCashBillTx(tx, bill, DrainGuards{IsolatedBalance: queries.GetIsolatedBalance, HasPendingIncoming: queries.HasPendingIncoming}, time.Now())
		})
		require.ErrorIs(t, err, ErrBillNotDrained)
		assert.EqualValues(t, 1, countRows(t, gormDB, &db.App{}, "id = ?", bill.ID),
			"a bill still holding funds must never be force-deleted")
	})

	t.Run("archives when genuinely drained", func(t *testing.T) {
		bill := newCashBill(t, gormDB, hub, 0, []db.CashWalletClaim{
			{AmountMloki: 1000, ClaimedAt: &claimed},
		})
		require.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
			return ArchiveAndDeleteDrainedCashBillTx(tx, bill, DrainGuards{IsolatedBalance: queries.GetIsolatedBalance, HasPendingIncoming: queries.HasPendingIncoming}, time.Now())
		}))
		assert.Zero(t, countRows(t, gormDB, &db.App{}, "id = ?", bill.ID))

		var archived db.CashBillArchive
		require.NoError(t, gormDB.Where("wallet_app_id = ?", bill.ID).First(&archived).Error)
		assert.Equal(t, db.CashBillOutcomeDrained, archived.Outcome)
	})
}

// TestArchiveAndDeleteCashBillTx_CarriesPayoutFacts: a redeemed slice must
// archive with its proof of payment, since the transaction rows that held it
// cascade away with the bill.
func TestArchiveAndDeleteCashBillTx_CarriesPayoutFacts(t *testing.T) {
	gormDB := newArchiveTestDB(t)
	hub := newHub(t, gormDB)

	claimed := time.Now().Add(-time.Minute)
	settled := claimed.Add(time.Second)
	hash := randomHex32(t)
	bill := newCashBill(t, gormDB, hub, 0, []db.CashWalletClaim{{
		AmountMloki:     1000,
		ClaimedAt:       &claimed,
		PaymentHash:     hash,
		Preimage:        "preimage-abc",
		RedeemFeeMloki:  7,
		RoutingFeeMloki: 3,
		SettledAt:       &settled,
	}})

	require.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
		return ArchiveAndDeleteCashBillTx(tx, bill, db.CashBillOutcomeDrained, 0, time.Now())
	}))

	slices := archivedSlices(t, gormDB, bill.ID)
	require.Len(t, slices, 1)
	assert.Equal(t, db.CashSliceStatusRedeemed, slices[0].Outcome)
	assert.Equal(t, hash, slices[0].PaymentHash)
	assert.Equal(t, "preimage-abc", slices[0].Preimage)
	assert.EqualValues(t, 7, slices[0].RedeemFeeMloki, "the hub's cut, borne by the recipient")
	assert.EqualValues(t, 3, slices[0].RoutingFeeMloki, "what the hub paid the network")
	assert.NotNil(t, slices[0].SettledAt)
}

// TestDeriveSliceOutcome covers the mapping directly, including the pairs that
// are easy to conflate.
func TestDeriveSliceOutcome(t *testing.T) {
	claimed := time.Now()
	spunOff := uint(7)

	cases := []struct {
		name  string
		claim db.CashWalletClaim
		bill  string
		want  string
	}{
		{"claimed and spun off is a split", db.CashWalletClaim{ClaimedAt: &claimed, SpunOffToWalletAppID: &spunOff}, db.CashBillOutcomeDrained, db.CashSliceStatusSplit},
		{"claimed without a target was redeemed", db.CashWalletClaim{ClaimedAt: &claimed}, db.CashBillOutcomeDrained, db.CashSliceStatusRedeemed},
		{"a claimed slice ignores the bill outcome", db.CashWalletClaim{ClaimedAt: &claimed}, db.CashBillOutcomeExpired, db.CashSliceStatusRedeemed},
		{"unclaimed on an expired bill", db.CashWalletClaim{}, db.CashBillOutcomeExpired, db.CashSliceStatusExpired},
		{"unclaimed on a written-off bill", db.CashWalletClaim{}, db.CashBillOutcomeWrittenOff, db.CashSliceStatusWrittenOff},
		{"unclaimed on a void bill", db.CashWalletClaim{}, db.CashBillOutcomeVoid, db.CashSliceStatusVoid},
		// The distinction that matters: an operator deleting a bill cuts a
		// recipient off before their window passed, which is not "expired".
		{"unclaimed on an operator-deleted bill is reclaimed", db.CashWalletClaim{}, db.CashBillOutcomeDeleted, db.CashSliceStatusReclaimed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, DeriveSliceOutcome(tc.claim, tc.bill))
		})
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
