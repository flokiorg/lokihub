package migrations

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// newAlphaDB builds the apps/app_permissions schema a 0.3.0-alpha install has:
// `isolated` bool, and none of kind / parent_app_id / parent_kind / expires_at /
// cleanup_in_progress. Those columns only ever arrived via AutoMigrate, at the
// very end of Migrate(), so any earlier migration touching them must cope with
// their absence.
func newAlphaDB(t *testing.T) *gorm.DB {
	t.Helper()
	uri := filepath.Join(t.TempDir(), "alpha.db")
	gormDB, err := db.NewDBWithConfig(&db.Config{URI: uri})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(gormDB) })

	// Verbatim DDL GORM's AutoMigrate produced for 0.3.0-alpha. Kept on one
	// line: GORM's sqlite table-rebuild parser mishandles hand-formatted DDL.
	require.NoError(t, gormDB.Exec("CREATE TABLE `apps` (`id` integer PRIMARY KEY AUTOINCREMENT,`name` text,`description` text,`app_pubkey` text NOT NULL,`wallet_pubkey` text,`created_at` datetime,`updated_at` datetime,`last_used_at` datetime,`isolated` numeric,`metadata` JSON)").Error)
	require.NoError(t, gormDB.Exec("CREATE TABLE `app_permissions` (`id` integer PRIMARY KEY AUTOINCREMENT,`app_id` integer,`scope` text,`max_amount_loki` integer,`budget_renewal` text,`expires_at` datetime,`created_at` datetime,`updated_at` datetime,CONSTRAINT `fk_app_permissions_app` FOREIGN KEY (`app_id`) REFERENCES `apps`(`id`) ON DELETE CASCADE)").Error)
	require.NoError(t, gormDB.Exec(`INSERT INTO apps (id, name, app_pubkey, isolated) VALUES (1, 'plain', 'pk1', 0)`).Error)
	require.NoError(t, gormDB.Exec(`INSERT INTO apps (id, name, app_pubkey, isolated) VALUES (2, 'sandboxed', 'pk2', 1)`).Error)
	require.NoError(t, gormDB.Exec(`INSERT INTO app_permissions (app_id, scope) VALUES (1, 'get_info')`).Error)
	return gormDB
}

// A user going straight from 0.3.0-alpha to a 0.5.0 release candidate must
// reach the current schema. This failed at startup with
// "SQL logic error: no such column: parent_kind (1)".
func TestMigrate_FromAlphaSchema(t *testing.T) {
	gormDB := newAlphaDB(t)

	require.NoError(t, Migrate(gormDB))

	for _, col := range []string{"kind", "parent_app_id", "parent_kind", "split_from_wallet_app_id"} {
		assert.Truef(t, gormDB.Migrator().HasColumn("apps", col), "apps.%s missing after migrate", col)
	}
	assert.False(t, gormDB.Migrator().HasColumn("apps", "isolated"))

	var kinds []string
	require.NoError(t, gormDB.Table("apps").Order("id").Pluck("kind", &kinds).Error)
	assert.Equal(t, []string{"standard", "isolated"}, kinds)

	// Running again on the migrated DB must stay a no-op.
	require.NoError(t, Migrate(gormDB))
}
