package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// Two Hub processes sharing one postgres database each have their own connection
// pool, so whatever keeps circle-wallet creation safe has to hold across pools,
// not just across goroutines in one process. These tests split the race of the
// separate-controllers tests across two pools on the same database. Postgres
// only: a sqlite file has no second process to share it with.

// secondProcess is svc as a second Hub process would see it: the same database
// through its own connection pool.
func secondProcess(t *testing.T, svc *tests.TestService) *tests.TestService {
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

// raceCircleJoinsAcross runs every request at once, alternating between the two
// processes' pools, each request through its own controller.
func raceCircleJoinsAcross(t *testing.T, procs []*tests.TestService, provider *db.App, requests []string) []string {
	t.Helper()
	type prepared struct {
		ctl     *nip47Controller
		req     *models.Request
		eventID uint
	}
	ps := make([]prepared, len(requests))
	for i, raw := range requests {
		proc := procs[i%len(procs)]
		req := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(raw), req))
		ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
		require.NoError(t, proc.DB.Create(ev).Error)
		ps[i] = prepared{NewTestNip47ControllerWithSocialCache(proc, &mockSocialCache{authorized: true}), req, ev.ID}
	}
	ready := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var outcomes []string
	for _, p := range ps {
		wg.Add(1)
		go func(p prepared) {
			defer wg.Done()
			<-ready
			p.ctl.testJoin(t, context.TODO(), p.req, p.eventID, provider, func(r *models.Response, _ nostr.Tags) {
				mu.Lock()
				defer mu.Unlock()
				if r.Error != nil {
					outcomes = append(outcomes, r.Error.Code)
				} else {
					outcomes = append(outcomes, "OK")
				}
			})
		}(p)
	}
	close(ready)
	wg.Wait()
	return outcomes
}

// One wallet per identity holds across processes, and a refused join leaves no
// orphan wallet behind in either.
func TestCreateCircleWallet_TwoProcesses_SameIdentity_ExactlyOneWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	other := secondProcess(t, svc)
	provider := createCircleHub(t, svc, 7200, 100_000_000)

	const rounds, perRound = 10, 6
	for round := 0; round < rounds; round++ {
		key := nostr.GeneratePrivateKey()
		requests := make([]string, perRound)
		for i := range requests {
			requests[i] = distinctCircleWalletRequest(t, key, *provider.WalletPubkey, fmt.Sprintf("r%d-%d", round, i), 50_000, 3600)
		}
		outcomes := raceCircleJoinsAcross(t, []*tests.TestService{svc, other}, provider, requests)

		pub, _ := nostr.GetPublicKey(key)
		var memberships int64
		require.NoError(t, svc.DB.Model(&db.CircleWalletMembership{}).
			Where("circle_hub_app_id = ? AND requester_pubkey = ?", provider.ID, pub).Count(&memberships).Error)
		require.EqualValues(t, 1, memberships, "round %d: outcomes %v", round, outcomes)
	}

	var wallets, memberships int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Count(&wallets).Error)
	require.NoError(t, svc.DB.Model(&db.CircleWalletMembership{}).Where("circle_hub_app_id = ?", provider.ID).Count(&memberships).Error)
	require.Equal(t, memberships, wallets, "a refused join must leave no orphan wallet behind")
}
