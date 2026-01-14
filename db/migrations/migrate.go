package migrations

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func Migrate(gormDB *gorm.DB) error {

	m := gormigrate.New(gormDB, gormigrate.DefaultOptions, []*gormigrate.Migration{
		_202401191539_initial_migration,
		_202501141200_add_lsps_table,
	})

	return m.Migrate()
}

type sqlDialectDef struct {
	Timestamp               string
	AutoincrementPrimaryKey string
	DropTableCascade        string
}

var sqlDialectSqlite = sqlDialectDef{
	Timestamp:               "datetime",
	AutoincrementPrimaryKey: "INTEGER PRIMARY KEY AUTOINCREMENT",
	DropTableCascade:        "",
}

var sqlDialectPostgres = sqlDialectDef{
	Timestamp:               "timestamptz",
	AutoincrementPrimaryKey: "SERIAL PRIMARY KEY",
	DropTableCascade:        "CASCADE",
}

func getDialect(tx *gorm.DB) *sqlDialectDef {
	switch tx.Dialector.Name() {
	case "postgres":
		return &sqlDialectPostgres
	default:
		return &sqlDialectSqlite
	}
}

func exec(tx *gorm.DB, templ *template.Template) error {
	dialect := getDialect(tx)
	var buf strings.Builder
	err := templ.Execute(&buf, dialect)
	if err != nil {
		panic(fmt.Sprintf("failed to render SQL template: %v", err))
	}

	return tx.Exec(buf.String()).Error
}
