package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// Regression test for a real deadlock found via live-instance testing and
// confirmed reproducible in this package's own test suite: CreateAppTx
// (apps/apps_service.go), the only path create_circle_wallet uses to create
// a child, ran its prepareApp uniqueness-check reads (name/pubkey) through
// svc.db instead of the caller's own already-open tx. Those reads then raced
// the still-open outer transaction create_circle_wallet_controller.go holds
// for its atomic commitment-check + membership-insert — contending on the
// same underlying SQLite file lock from two different connections, which
// stalled indefinitely under load rather than failing fast. Fixed by
// threading a queryDB parameter through prepareApp so CreateAppTx's reads use
// the caller's own tx, never a separate connection.
//
// A single serial call rarely triggers the stall (there's no contending
// transaction to race against) - reproducing it needs multiple overlapping
// CreateAppTx calls contending for the pool at once. The controller's own
// in-process guard (activeCircleInvoices) serializes concurrent creates
// against one hub, so this uses a separate hub per goroutine instead - still
// many overlapping controller.db.Transaction calls hitting the same
// underlying *gorm.DB/connection pool at once, which is what actually
// produces the contention, not sharing one hub. On the buggy code, this test
// used to hang past its own bound (observed taking upwards of 120s+ once
// several tests had already exercised the same pool in a single test binary
// run); on the fixed code every response arrives well within the bound below.
func TestHandleCreateCircleWalletEvent_ConcurrentCreations_NeverStall(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const goroutines = 12
	controller := NewTestNip47ControllerWithSocialCache(svc, &mockSocialCache{authorized: true})

	type prepared struct {
		req     *models.Request
		eventID uint
		hub     *db.App
	}
	work := make([]prepared, goroutines)
	for i := 0; i < goroutines; i++ {
		hub := createCircleHub(t, svc, 7200, 200_000)
		requesterKey := nostr.GeneratePrivateKey()
		req := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(makeCircleWalletRequest(t, requesterKey, hub.AppPubkey, 100_000, 3600)), req))
		ev := &db.RequestEvent{NostrId: nostr.GeneratePrivateKey()}
		require.NoError(t, svc.DB.Create(&ev).Error)
		work[i] = prepared{req: req, eventID: ev.ID, hub: hub}
	}

	responses := make(chan *models.Response, goroutines)
	ready := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		w := work[i]
		go func() {
			<-ready
			controller.HandleCreateCircleWalletEvent(context.TODO(), w.req, w.eventID, w.hub,
				func(r *models.Response, _ nostr.Tags) { responses <- r })
		}()
	}
	close(ready)

	const bound = 20 * time.Second
	deadline := time.After(bound)
	received := 0
	for received < goroutines {
		select {
		case resp := <-responses:
			require.Nil(t, resp.Error, "concurrent circle wallet creation for a distinct identity must not fail")
			received++
		case <-deadline:
			t.Fatalf("only received %d/%d responses within %s - a concurrent create_circle_wallet appears stalled, likely prepareApp's uniqueness-check reads racing another goroutine's open transaction via a separate DB handle again", received, goroutines, bound)
		}
	}
}
