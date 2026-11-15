package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
)

// SecondProcess is svc as a second Hub process would see it: the same database
// through its own connection pool. Two Hub processes sharing one postgres
// database each have their own pool, so whatever keeps a race safe has to hold
// across pools, not just across goroutines in one process. Postgres only: a
// sqlite file has no second process to share it with, so the test is skipped.
func SecondProcess(t *testing.T, svc *TestService) *TestService {
	t.Helper()
	dialector, ok := svc.DB.Dialector.(*postgres.Dialector)
	if !ok {
		t.Skip("postgres only: run with TEST_DATABASE_URI pointing at a postgres database")
	}
	pool, err := db.NewDBWithConfig(&db.Config{URI: dialector.DSN})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Stop(pool) })
	other := *svc
	other.DB = pool
	other.AppsService = apps.NewAppsService(pool, svc.EventPublisher, svc.Keys, svc.Cfg)
	return &other
}
