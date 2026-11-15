package db

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/logger"
)

// sqlite allows one writer at a time, and a connection waiting for the write
// lock does not queue: its busy handler sleeps and polls until busy_timeout.
// With several write transactions in flight, a poller can lose every race for
// the whole timeout while the others commit in milliseconds, and fails with
// SQLITE_BUSY ("database is locked"). writeQueue makes write transactions
// queue in the process instead, in arrival order, before they take a
// connection: the one holding the write lock is then the only one asking
// sqlite for it.
const (
	// sqliteIdleConns keeps enough connections open that ordinary concurrency
	// does not close and reopen them on every burst.
	sqliteIdleConns = 16
	// writeQueueWait bounds how long a transaction waits for its turn. Past it
	// the transaction goes to sqlite anyway, where busy_timeout still applies:
	// a holder that never ends (or a transaction begun inside another on the
	// same handle) costs a wait, never a deadlock.
	writeQueueWait = 30 * time.Second
)

// serialPool is the sqlite connection pool gorm sees: *sql.DB with BeginTx
// taking a turn in the write queue first. On sqlite every transaction is a
// write transaction (_txlock=immediate).
type serialPool struct {
	*sql.DB
	turn chan struct{}
}

func newSerialPool(sqlDB *sql.DB) *serialPool {
	return &serialPool{DB: sqlDB, turn: make(chan struct{}, 1)}
}

// BeginTx shadows (*sql.DB).BeginTx, so gorm begins through this one
// (gorm.ConnPoolBeginner).
func (p *serialPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	release, err := p.wait(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := p.DB.BeginTx(ctx, opts)
	if err != nil {
		release()
		return nil, err
	}
	return &serialTx{Tx: tx, db: p.DB, release: release}, nil
}

// wait takes this transaction's turn; the returned func gives it back.
func (p *serialPool) wait(ctx context.Context) (func(), error) {
	timer := time.NewTimer(writeQueueWait)
	defer timer.Stop()
	select {
	case p.turn <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-p.turn }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		logger.Logger.Warn().Dur("waited", writeQueueWait).
			Msg("A database transaction waited its whole turn in the write queue; beginning it without one")
		return func() {}, nil
	}
}

func (p *serialPool) GetDBConn() (*sql.DB, error) { return p.DB, nil }

// serialTx gives the write queue's turn back when the transaction ends, once,
// whether it commits, rolls back, or both (gorm rolls back after a failed
// commit).
type serialTx struct {
	*sql.Tx
	db      *sql.DB
	release func()
}

func (t *serialTx) Commit() error {
	defer t.release()
	return t.Tx.Commit()
}

func (t *serialTx) Rollback() error {
	defer t.release()
	return t.Tx.Rollback()
}

func (t *serialTx) GetDBConn() (*sql.DB, error) { return t.db, nil }

// openSqliteFile opens a file database: the write queue, a pool that keeps its
// connections, and auto_vacuum set once. auto_vacuum is not a per-connection
// pragma: setting it runs a write transaction, so in the connection string it
// made every new connection, read-only ones included, wait for the write lock
// before its first statement. It only takes effect on a database with no
// tables, which is when it is set here.
func openSqliteFile(driverName, dsn string) (*serialPool, error) {
	sqlDB, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxIdleConns(sqliteIdleConns)
	if err := setAutoVacuumOnEmpty(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return newSerialPool(sqlDB), nil
}

func setAutoVacuumOnEmpty(sqlDB *sql.DB) error {
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var tables int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&tables); err != nil {
		return fmt.Errorf("read the schema: %w", err)
	}
	if tables > 0 {
		return nil
	}
	// VACUUM writes the header, so the setting holds for every connection, not
	// only this one.
	for _, stmt := range []string{"PRAGMA auto_vacuum = FULL", "VACUUM"} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}
