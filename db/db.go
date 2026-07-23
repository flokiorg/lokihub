package db

import (
	"fmt"
	"log"
	"os"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gorm_logger "gorm.io/gorm/logger"

	sqlite_wrapper "github.com/flokiorg/lokihub/db/sqlite-wrapper"
	"github.com/flokiorg/lokihub/logger"
)

type Config struct {
	URI        string
	LogQueries bool
	DriverName string
}

func NewDB(uri string, logDBQueries bool) (*gorm.DB, error) {
	return NewDBWithConfig(&Config{
		URI:        uri,
		LogQueries: logDBQueries,
		DriverName: "",
	})
}

func NewDBWithConfig(cfg *Config) (*gorm.DB, error) {
	gormConfig := &gorm.Config{
		TranslateError: true,
	}
	// Always install our own logger rather than leaving it nil: a nil Logger
	// makes GORM fall back to its package-level default, which logs at Warn
	// level and does not ignore record-not-found errors. That turns expected
	// existence checks (duplicate name/pubkey lookups, allocation lookups)
	// into error-shaped log lines even when query logging is off. Error level
	// keeps genuine errors (constraint violations, etc.) visible either way.
	logLevel := gorm_logger.Error
	if cfg.LogQueries {
		logLevel = gorm_logger.Info
	}
	gormConfig.Logger = gorm_logger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), gorm_logger.Config{
		LogLevel:                  logLevel,
		IgnoreRecordNotFoundError: true,
	})

	var ret *gorm.DB

	if IsPostgresURI(cfg.URI) {
		pgConfig := postgres.Config{
			DriverName: cfg.DriverName,
			DSN:        cfg.URI,
		}
		var err error
		ret, err = newPostgresDB(pgConfig, gormConfig)
		if err != nil {
			return nil, err
		}
	} else {
		sqliteURI := cfg.URI

		// apply pragma if we're not running the tests
		if !strings.Contains(sqliteURI, "?mode=memory") {
			// modernc.org/sqlite only understands _txlock and _pragma=KEY(VALUE) — mattn-style
			// shorthand params (_journal_mode, _busy_timeout, etc.) are silently ignored.
			// _txlock=immediate: start all transactions IMMEDIATE to avoid SQLITE_BUSY on concurrent writes
			// _pragma=busy_timeout(5000): wait up to 5s before returning SQLITE_BUSY
			// _pragma=journal_mode(WAL): readers do not block writers and vice-versa
			// _pragma=foreign_keys(1): enforce FK constraints
			// _pragma=auto_vacuum(1): reclaim disk space on DELETE
			// _pragma=synchronous(NORMAL): less frequent sync, safe with WAL
			// _pragma=cache_size(-20000): 20 MB memory cache
			// _pragma=temp_store(MEMORY): keep temp tables in memory (required for modernc.org/sqlite)
			sqliteURI = sqliteURI + "?_txlock=immediate" +
				"&_pragma=busy_timeout(5000)" +
				"&_pragma=journal_mode(WAL)" +
				"&_pragma=foreign_keys(1)" +
				"&_pragma=auto_vacuum(1)" +
				"&_pragma=synchronous(NORMAL)" +
				"&_pragma=cache_size(-20000)" +
				"&_pragma=temp_store(MEMORY)"
		}

		driverName := sqlite_wrapper.Sqlite3WrapperDriverName
		if cfg.DriverName != "" {
			driverName = cfg.DriverName
		}

		sqliteConfig := sqlite.Config{
			DriverName: driverName,
			DSN:        sqliteURI,
		}

		var err error
		ret, err = newSqliteDB(sqliteConfig, gormConfig)
		if err != nil {
			return nil, err
		}
	}

	logger.Logger.Debug().Str("db_backend", ret.Dialector.Name()).Msg("loaded database")

	return ret, nil
}

func newSqliteDB(sqliteConfig sqlite.Config, gormConfig *gorm.Config) (*gorm.DB, error) {
	gormDB, err := gorm.Open(sqlite.New(sqliteConfig), gormConfig)
	if err != nil {
		return nil, err
	}

	return gormDB, nil
}

func newPostgresDB(pgConfig postgres.Config, gormConfig *gorm.Config) (*gorm.DB, error) {
	gormDB, err := gorm.Open(postgres.New(pgConfig), gormConfig)
	if err != nil {
		return nil, err
	}

	return gormDB, nil
}

func Stop(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get database connection: %w", err)
	}

	dbBackend := db.Name()
	logger.Logger.Debug().Str("db_backend", dbBackend).Msg("shutting down database")
	if dbBackend == "sqlite" {
		err = db.Exec("PRAGMA wal_checkpoint(FULL)", nil).Error
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to execute wal endpoint")
		}
	}

	err = sqlDB.Close()
	if err != nil {
		return fmt.Errorf("failed to close database connection: %w", err)
	}
	return nil
}

func IsPostgresURI(uri string) bool {
	return strings.HasPrefix(uri, "postgresql://") ||
		strings.HasPrefix(uri, "postgres://") // Schema used by the "testdb" package.
}
