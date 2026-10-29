package migrations_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/migrations"
	"github.com/flokiorg/lokihub/logger"
	testsdb "github.com/flokiorg/lokihub/tests/db"
)

// U-1.R2a: the package converts no data. Its only Migrate* function is Migrate.
func TestU_1_R2a_NoConversionMigrations(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var found []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && strings.HasPrefix(fn.Name.Name, "Migrate") {
				found = append(found, fn.Name.Name)
			}
		}
	}
	require.Equal(t, []string{"Migrate"}, found)
}

type foreignKey struct {
	Table, Column, RefTable, OnDelete string
}

func expectedForeignKeys(t *testing.T, gormDB *gorm.DB) []foreignKey {
	t.Helper()
	var fks []foreignKey
	cache := &sync.Map{}
	for _, m := range migrations.Models() {
		s, err := schema.Parse(m, cache, gormDB.NamingStrategy)
		require.NoError(t, err)
		for _, rel := range s.Relationships.Relations {
			c := rel.ParseConstraint()
			if c == nil || c.Schema != s {
				continue
			}
			onDelete := strings.ToUpper(c.OnDelete)
			if onDelete == "" {
				onDelete = "NO ACTION"
			}
			fks = append(fks, foreignKey{s.Table, c.ForeignKeys[0].DBName, c.ReferenceSchema.Table, onDelete})
		}
	}
	sortKeys(fks)
	return fks
}

func actualForeignKeys(t *testing.T, gormDB *gorm.DB) []foreignKey {
	t.Helper()
	var fks []foreignKey
	tables, err := gormDB.Migrator().GetTables()
	require.NoError(t, err)
	for _, table := range tables {
		var rows []struct {
			Column, RefTable, OnDelete string
		}
		if gormDB.Name() == "postgres" {
			err = gormDB.Raw(`
				SELECT kcu.column_name AS "column", ccu.table_name AS ref_table, rc.delete_rule AS on_delete
				FROM information_schema.table_constraints tc
				JOIN information_schema.key_column_usage kcu ON kcu.constraint_name = tc.constraint_name AND kcu.table_schema = tc.table_schema
				JOIN information_schema.referential_constraints rc ON rc.constraint_name = tc.constraint_name AND rc.constraint_schema = tc.table_schema
				JOIN information_schema.constraint_column_usage ccu ON ccu.constraint_name = tc.constraint_name AND ccu.table_schema = tc.table_schema
				WHERE tc.table_name = ? AND tc.constraint_type = 'FOREIGN KEY' AND tc.table_schema = current_schema()`, table).Scan(&rows).Error
		} else {
			err = gormDB.Raw(`SELECT "from" AS "column", "table" AS ref_table, on_delete FROM pragma_foreign_key_list(?)`, table).Scan(&rows).Error
		}
		require.NoError(t, err)
		for _, r := range rows {
			fks = append(fks, foreignKey{table, r.Column, r.RefTable, strings.ToUpper(r.OnDelete)})
		}
	}
	sortKeys(fks)
	return fks
}

func sortKeys(fks []foreignKey) {
	sort.Slice(fks, func(i, j int) bool {
		a, b := fks[i], fks[j]
		return a.Table+"."+a.Column < b.Table+"."+b.Column
	})
}

// U-1.R2a: a fresh schema (sqlite, or postgres under TEST_DATABASE_URI) is exactly
// the model list: every table, every foreign key with its delete action, and the
// two partial indexes.
func TestU_1_R2a_FreshSchemaMatchesModels(t *testing.T) {
	logger.Init("4")
	gormDB, err := testsdb.NewDB(t)
	require.NoError(t, err)
	defer testsdb.CloseDB(gormDB)

	for _, m := range migrations.Models() {
		require.True(t, gormDB.Migrator().HasTable(m), "%T", m)
	}
	expected := expectedForeignKeys(t, gormDB)
	require.NotEmpty(t, expected)
	require.Equal(t, expected, actualForeignKeys(t, gormDB))
	require.True(t, gormDB.Migrator().HasIndex(&db.App{}, "idx_apps_cleanup"))
	require.True(t, gormDB.Migrator().HasIndex(&db.App{}, "idx_apps_app_pubkey_unique"))

	err = gormDB.Create(&db.App{Name: "a", AppPubkey: "aa", Kind: db.AppKindIsolated}).Error
	require.NoError(t, err)
	err = gormDB.Create(&db.App{Name: "b", AppPubkey: "aa", Kind: db.AppKindIsolated}).Error
	require.True(t, db.IsDuplicateKeyError(err), "the unique app_pubkey index refuses a duplicate, got %v", err)

	require.NoError(t, migrations.Migrate(gormDB), "a second run on the current schema is a no-op")
}

// WP-1 item 8: a new database is marked with the schema epoch; one holding apps but
// no mark predates it and is refused; an empty old database is adopted.
func TestSchemaEpochGuard(t *testing.T) {
	logger.Init("4")
	gormDB, err := testsdb.NewDB(t)
	require.NoError(t, err)
	defer testsdb.CloseDB(gormDB)

	var cfg db.UserConfig
	require.NoError(t, gormDB.Where(&db.UserConfig{Key: migrations.SchemaEpochKey}).First(&cfg).Error)
	require.Equal(t, migrations.SchemaEpoch, cfg.Value)

	require.NoError(t, gormDB.Create(&db.App{Name: "a", AppPubkey: "aa", Kind: db.AppKindIsolated}).Error)
	require.NoError(t, migrations.Migrate(gormDB), "a marked database with apps starts")

	// The same database without its mark is an rc.19 database.
	require.NoError(t, gormDB.Where(&db.UserConfig{Key: migrations.SchemaEpochKey}).Delete(&db.UserConfig{}).Error)
	require.ErrorIs(t, migrations.Migrate(gormDB), migrations.ErrDatabasePredatesSchema)

	// Another epoch is refused too.
	require.NoError(t, gormDB.Create(&db.UserConfig{Key: migrations.SchemaEpochKey, Value: "older"}).Error)
	require.Error(t, migrations.Migrate(gormDB))

	// No apps and no mark: an empty database, adopted and marked.
	require.NoError(t, gormDB.Where(&db.UserConfig{Key: migrations.SchemaEpochKey}).Delete(&db.UserConfig{}).Error)
	require.NoError(t, gormDB.Where("1 = 1").Delete(&db.App{}).Error)
	require.NoError(t, migrations.Migrate(gormDB))
	var again db.UserConfig
	require.NoError(t, gormDB.Where(&db.UserConfig{Key: migrations.SchemaEpochKey}).First(&again).Error)
	require.Equal(t, migrations.SchemaEpoch, again.Value)
}
