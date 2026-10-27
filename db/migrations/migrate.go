// Package migrations creates lokihub's schema. There are no data conversions: a
// database is created fresh, and one made before the current schema epoch is
// refused rather than converted.
package migrations

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// SchemaEpochKey is the user_configs key recording the schema a database was
// created with.
const SchemaEpochKey = "SchemaEpoch"

// SchemaEpoch is the current schema. A database without it that already holds
// apps predates it and is refused.
const SchemaEpoch = "cash-final-1"

// ErrDatabasePredatesSchema refuses a database made before SchemaEpoch.
var ErrDatabasePredatesSchema = errors.New("database predates " + SchemaEpoch + "; reset it (no migration is provided)")

// Models are every table Migrate creates. The LSP and Identity Authority tables
// are created by their own services.
func Models() []any {
	return []any{
		&db.UserConfig{},
		&db.App{},
		&db.AppPermission{},
		&db.RequestEvent{},
		&db.ResponseEvent{},
		&db.Transaction{},
		&db.Swap{},
		&db.Forward{},
		&db.CircleIdentity{},
		&db.CircleIdentityAllowedPubkey{},
		&db.CashHubConfig{},
		&db.CircleHubConfig{},
		&db.CashWalletClaim{},
		&db.CircleWalletIdentityProof{},
		&db.CircleWalletMembership{},
		&db.CashTransferProof{},
		&db.CashStrandedFund{},
		&db.CashBillArchive{},
		&db.CashBillSliceArchive{},
	}
}

// Migrate refuses a database older than SchemaEpoch, then creates every table, the
// partial indexes and, on a new database, the epoch mark.
func Migrate(gormDB *gorm.DB) error {
	epoch, err := storedEpoch(gormDB)
	if err != nil {
		return err
	}
	if epoch == "" {
		hasApps, err := holdsApps(gormDB)
		if err != nil {
			return err
		}
		if hasApps {
			return ErrDatabasePredatesSchema
		}
	} else if epoch != SchemaEpoch {
		return fmt.Errorf("database schema %q is not %s; reset it (no migration is provided)", epoch, SchemaEpoch)
	}

	if err := gormDB.AutoMigrate(Models()...); err != nil {
		return err
	}
	if err := EnsurePartialIndexes(gormDB); err != nil {
		return err
	}
	if epoch == "" {
		return gormDB.Create(&db.UserConfig{Key: SchemaEpochKey, Value: SchemaEpoch}).Error
	}
	return nil
}

func storedEpoch(gormDB *gorm.DB) (string, error) {
	if !gormDB.Migrator().HasTable(&db.UserConfig{}) {
		return "", nil
	}
	var rows []db.UserConfig
	if err := gormDB.Where(&db.UserConfig{Key: SchemaEpochKey}).Limit(1).Find(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Value, nil
}

func holdsApps(gormDB *gorm.DB) (bool, error) {
	if !gormDB.Migrator().HasTable("apps") {
		return false, nil
	}
	var n int64
	if err := gormDB.Table("apps").Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}
