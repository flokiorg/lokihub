package archive

import (
	"testing"
	"time"

	"github.com/flokiorg/lokihub/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The archive keeps the bill's own lokicash1... string verbatim, copied off the
// App row that held it in life rather than re-derived.
//
// Verbatim matters: the token embeds the relay hints in effect at mint time, so
// a later re-encoding could produce a different string from the one its holder
// actually has — which would defeat the point of keeping it, since the only use
// is matching a token from a support ticket against the record.
func TestArchiveAndDeleteCashBillTx_KeepsCashTokenVerbatim(t *testing.T) {
	gormDB := newArchiveTestDB(t)
	hub := newHub(t, gormDB)

	bill := newCashBill(t, gormDB, hub, 5000, []db.CashWalletClaim{{AmountMloki: 5000}})

	//nolint:gosec // G101: a bech32 token fixture for the archive round trip, not a credential
	const token = "lokicash1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzk0ju0000000000000000"
	require.NoError(t, gormDB.Model(&db.App{}).
		Where("id = ?", bill.ID).Update("cash_token", token).Error)
	bill.CashToken = token

	require.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
		return ArchiveAndDeleteCashBillTx(tx, bill, db.CashBillOutcomeExpired, 0, time.Now())
	}))

	var archived db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_app_id = ?", bill.ID).First(&archived).Error)
	assert.Equal(t, token, archived.CashToken,
		"the archive must keep the exact string the bill was issued with")
}

// A bill minted before the column existed archives cleanly with an empty token
// rather than failing — the audit copy is best-effort, never a gate on
// destroying a bill correctly.
func TestArchiveAndDeleteCashBillTx_MissingCashTokenIsNotFatal(t *testing.T) {
	gormDB := newArchiveTestDB(t)
	hub := newHub(t, gormDB)

	bill := newCashBill(t, gormDB, hub, 1000, []db.CashWalletClaim{{AmountMloki: 1000}})

	require.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
		return ArchiveAndDeleteCashBillTx(tx, bill, db.CashBillOutcomeDeleted, 0, time.Now())
	}))

	var archived db.CashBillArchive
	require.NoError(t, gormDB.Where("wallet_app_id = ?", bill.ID).First(&archived).Error)
	assert.Empty(t, archived.CashToken)
	assert.Equal(t, db.CashBillOutcomeDeleted, archived.Outcome,
		"the rest of the record must still be written")
}
