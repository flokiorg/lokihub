package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// Production builds a new controller for every request, so these tests race
// requests through separate controllers. Whatever keeps creation safe must
// therefore live in the database: the Hub-scoped transaction lock for the
// commitment check, and the membership unique index for one wallet per
// identity (NIP-CW §Creating a Circle Wallet, steps 8-9).

// raceCircleJoins runs every request at once, each through its own controller.
func raceCircleJoins(t *testing.T, svc *tests.TestService, provider *db.App, requests []string) []string {
	t.Helper()
	type prepared struct {
		ctl     *nip47Controller
		req     *models.Request
		eventID uint
	}
	ps := make([]prepared, len(requests))
	for i, raw := range requests {
		req := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(raw), req))
		ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
		require.NoError(t, svc.DB.Create(ev).Error)
		ps[i] = prepared{NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true}), req, ev.ID}
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

// CW-creating-a-circle-wallet-15: two in-flight requests must not both pass the
// commitment check against the same balance.
func TestCreateCircleWallet_SeparateControllers_BalanceCoversOne_AtMostOneCommitted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 100_000) // room for exactly one 100,000 mloki wallet

	requests := make([]string, 8)
	for i := range requests {
		requests[i] = makeCircleWalletRequest(t, nostr.GeneratePrivateKey(), *provider.WalletPubkey, 100_000, 3600)
	}
	outcomes := raceCircleJoins(t, svc, provider, requests)

	var wallets int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", provider.ID, db.AppKindCircleWallet).Count(&wallets).Error)
	require.EqualValues(t, 1, wallets, "outcomes %v", outcomes)
}

// CW-creating-a-circle-wallet-16: with a generous balance every request can pass
// the membership pre-check before any commits, so the unique index is all that
// is left, and a conflict must roll back the whole transaction, including the
// app it had just created.
func TestCreateCircleWallet_SeparateControllers_SameIdentity_ExactlyOneWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	provider := createCircleHub(t, svc, 7200, 100_000_000)

	// Several rounds: the window in which every request has passed the
	// pre-check but none has committed is narrow.
	const rounds, perRound = 10, 6
	for round := 0; round < rounds; round++ {
		key := nostr.GeneratePrivateKey()
		requests := make([]string, perRound)
		for i := range requests {
			requests[i] = distinctCircleWalletRequest(t, key, *provider.WalletPubkey, fmt.Sprintf("r%d-%d", round, i), 50_000, 3600)
		}
		outcomes := raceCircleJoins(t, svc, provider, requests)

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
