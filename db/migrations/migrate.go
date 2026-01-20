package migrations

import (
	"github.com/flokiorg/lokihub/db"
	"gorm.io/gorm"
)

func Migrate(gormDB *gorm.DB) error {
	// AutoMigrate all core models
	// Note: LSP model is migrated separately in LSPManager (via manager_db.go)
	return gormDB.AutoMigrate(
		&db.UserConfig{},
		&db.App{},
		&db.AppPermission{},
		&db.RequestEvent{},
		&db.ResponseEvent{},
		&db.Transaction{},
		&db.Swap{},
		&db.Forward{},
	)
}
