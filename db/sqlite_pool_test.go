package db

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type poolRow struct {
	ID uint
	V  string
}

func newFileDB(t *testing.T) *gorm.DB {
	t.Helper()
	gormDB, err := NewDBWithConfig(&Config{URI: filepath.Join(t.TempDir(), "pool.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = Stop(gormDB) })
	require.NoError(t, gormDB.AutoMigrate(&poolRow{}))
	return gormDB
}

// A fresh file database gets auto_vacuum FULL in its header, so every
// connection sees it although no connection string sets it any more.
func TestSqliteFile_AutoVacuumSetOnce(t *testing.T) {
	gormDB := newFileDB(t)
	sqlDB, err := gormDB.DB()
	require.NoError(t, err)
	conns := make([]interface{ Close() error }, 0, 3)
	for range 3 {
		conn, err := sqlDB.Conn(context.Background())
		require.NoError(t, err)
		conns = append(conns, conn)
		var mode int
		require.NoError(t, conn.QueryRowContext(context.Background(), "PRAGMA auto_vacuum").Scan(&mode))
		assert.Equal(t, 1, mode)
		var journal string
		require.NoError(t, conn.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journal))
		assert.Equal(t, "wal", journal)
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// A write transaction held past busy_timeout (5 s) no longer fails the writers
// and readers that arrive meanwhile with SQLITE_BUSY: writers queue for their
// turn, and opening a connection to read no longer needs the write lock.
func TestSqliteFile_WritersQueuePastBusyTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("holds a transaction for 6 s")
	}
	gormDB := newFileDB(t)
	held := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		assert.NoError(t, gormDB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&poolRow{V: "holder"}).Error; err != nil {
				return err
			}
			close(held)
			time.Sleep(6 * time.Second)
			return nil
		}))
	}()
	<-held
	errs := make(chan error, 16)
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs <- gormDB.Create(&poolRow{V: "writer"}).Error
		}()
		go func() {
			defer wg.Done()
			// Fresh connections: the pool's idle ones are all busy or new.
			var n int64
			errs <- gormDB.Model(&poolRow{}).Where("id > ?", i).Count(&n).Error
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	var n int64
	require.NoError(t, gormDB.Model(&poolRow{}).Count(&n).Error)
	assert.EqualValues(t, 9, n)
}

// Nested transactions use savepoints inside the one turn, a rolled-back
// transaction gives its turn back, and DB() works inside a transaction.
func TestSqliteFile_NestedRollbackAndDB(t *testing.T) {
	gormDB := newFileDB(t)
	boom := errors.New("boom")
	err := gormDB.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Create(&poolRow{V: "outer"}).Error)
		_, err := tx.DB()
		require.NoError(t, err)
		assert.ErrorIs(t, tx.Transaction(func(inner *gorm.DB) error {
			require.NoError(t, inner.Create(&poolRow{V: "inner"}).Error)
			return boom
		}), boom)
		return boom
	})
	require.ErrorIs(t, err, boom)

	done := make(chan error, 1)
	go func() { done <- gormDB.Create(&poolRow{V: "after"}).Error }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("the rolled-back transaction kept its turn")
	}
	var rows []poolRow
	require.NoError(t, gormDB.Find(&rows).Error)
	require.Len(t, rows, 1)
	assert.Equal(t, "after", rows[0].V)
}
