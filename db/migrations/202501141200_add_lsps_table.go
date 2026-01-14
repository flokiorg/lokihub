package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

type LSPSState struct {
	Key   string `gorm:"primaryKey"`
	Value []byte
}

var _202501141200_add_lsps_table = &gormigrate.Migration{
	ID: "202501141200_add_lsps_table",
	Migrate: func(tx *gorm.DB) error {
		return tx.AutoMigrate(&LSPSState{})
	},
	Rollback: func(tx *gorm.DB) error {
		return tx.Migrator().DropTable(&LSPSState{})
	},
}
