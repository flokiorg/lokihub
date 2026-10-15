package controllers

// mint_cash against one hub through separate controllers (one per request, as in
// production): cashwallet.LockHub must let at most one mint draw on the funds.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func TestMintCash_HubCap_SeparateControllers_AtMostOneFunded(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 200_000, "capfund") // same funding as TestHandleMintCashEvent_ConcurrentCreation_ExactlyOneSucceeds

	delay := 100 * time.Millisecond
	svc.LNClient.(*tests.MockLn).PaymentDelay = &delay

	const n = 4
	reqs := make([]*models.Request, n)
	ids := make([]uint, n)
	ctls := make([]*nip47Controller, n)
	for i := 0; i < n; i++ {
		pub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
		r := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(makeCashWalletRequest(pub, 50_000, 1800)), r))
		reqs[i] = r
		ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
		require.NoError(t, svc.DB.Create(ev).Error)
		ids[i] = ev.ID
		ctls[i] = NewTestNip47Controller(svc) // one per request, like production
	}
	ready := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var codes []string
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-ready
			ctls[i].HandleMintCashEvent(ctx, reqs[i], ids[i], hub, func(r *models.Response, _ nostr.Tags) {
				mu.Lock()
				defer mu.Unlock()
				if r.Error == nil {
					codes = append(codes, "OK")
				} else {
					codes = append(codes, r.Error.Code)
				}
			})
		}(i)
	}
	close(ready)
	wg.Wait()
	t.Logf("outcomes: %v", codes)

	ok, notReady := 0, 0
	for _, c := range codes {
		switch c {
		case "OK":
			ok++
		case "NOT_READY":
			notReady++
		}
	}
	if ok != 1 || notReady != n-1 {
		t.Fatalf("hub creation slot did not serialize separate controllers: %d OK, %d NOT_READY of %d", ok, notReady, n)
	}
}
