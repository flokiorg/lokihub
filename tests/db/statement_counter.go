package db

import (
	"context"
	"sync/atomic"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// StatementCounter counts SQL statements a gorm handle executes.
//
// It exists because wall-clock alone cannot see an N+1 on sqlite: the round
// trip is a function call rather than a socket, so a query-per-row loop can
// look acceptable in a benchmark and then collapse on postgres. Counting
// statements makes the real property assertable — "this does not grow with row
// count" — rather than something a reviewer has to infer from a timing.
//
// It hooks gorm's Logger.Trace, which fires exactly once per executed
// statement whatever the dialect.
type StatementCounter struct {
	inner gormlogger.Interface
	// Shared by pointer across every logger gorm derives from this one: gorm
	// calls LogMode when it builds a session, and a per-copy counter would
	// silently drop the statements made through those sessions.
	n *atomic.Int64
}

// CountStatements attaches a counter to db, returning the counter. It mutates
// the handle's logger, so callers should use it on a handle they own — which
// in practice is every test handle, since NewDB builds a fresh one per test.
func CountStatements(db *gorm.DB) *StatementCounter {
	c := &StatementCounter{inner: db.Logger, n: &atomic.Int64{}}
	db.Logger = c
	return c
}

// N reports the statements executed so far.
func (c *StatementCounter) N() int64 { return c.n.Load() }

// Reset zeroes the count, for measuring one operation at a time.
func (c *StatementCounter) Reset() { c.n.Store(0) }

func (c *StatementCounter) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return &StatementCounter{inner: c.inner.LogMode(level), n: c.n}
}

func (c *StatementCounter) Info(ctx context.Context, msg string, data ...interface{}) {
	c.inner.Info(ctx, msg, data...)
}

func (c *StatementCounter) Warn(ctx context.Context, msg string, data ...interface{}) {
	c.inner.Warn(ctx, msg, data...)
}

func (c *StatementCounter) Error(ctx context.Context, msg string, data ...interface{}) {
	c.inner.Error(ctx, msg, data...)
}

func (c *StatementCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	c.n.Add(1)
	// Deliberately NOT delegating: the inner logger is built with
	// LogQueries:true, so echoing a million statements would dominate the
	// measurement this type exists to take.
}
