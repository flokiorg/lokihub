package controllers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// TestHandleCreateJITWalletEvent_ConcurrentCreation_ExactlyOneSucceeds is a
// regression test for jitwallet.LockHub: create_jit_wallet previously had no
// concurrency guard at all (unlike create_circle_wallet's
// activeCircleInvoices), so two concurrent requests against the same jit_hub
// could both pass jitwallet.Resolve's balance pre-check against the same
// stale balance before either one's Commit actually transferred funds out -
// a real TOCTOU gap between the pre-check and the transfer, only
// incidentally prevented today by DB-level serialization (Postgres advisory
// lock / SQLite single-writer), not by any deliberate application-level
// guarantee. With the fix, exactly one concurrent request wins the hub's
// creation slot and the other is rejected outright before it ever reaches
// Resolve/Commit.
//
// mockLn.PaymentDelay stalls the winning goroutine's SendPaymentSync call
// just long enough for the losing goroutine's HandleCreateJITWalletEvent
// call to reach jitwallet.LockHub while the winner still holds it -
// otherwise, on a fast in-memory test DB, the two calls tend to complete
// fully sequentially (lock acquired-and-released before the second even
// starts), which would exercise the same DB-level serialization this test
// is specifically meant to route around, not the new in-process guard.
func TestHandleCreateJITWalletEvent_ConcurrentCreation_ExactlyOneSucceeds(t *testing.T) {
	ctx := context.TODO()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Ample balance for one 50_000-mloki wallet plus fee-reserve headroom
	// (not exercising fee/balance math here, just the lock).
	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 200_000, "fundtxhash")

	delay := 100 * time.Millisecond
	svc.LNClient.(*tests.MockLn).PaymentDelay = &delay

	controller := NewTestNip47Controller(svc)

	newRequest := func() *models.Request {
		beneficiaryKey := nostr.GeneratePrivateKey()
		beneficiaryPubkey, _ := nostr.GetPublicKey(beneficiaryKey)
		req := &models.Request{}
		require.NoError(t, json.Unmarshal([]byte(makeJITWalletRequest(beneficiaryPubkey, 50_000, 1800)), req))
		return req
	}
	newEvent := func() uint {
		ev := &db.RequestEvent{NostrId: tests.RandomHex32()}
		require.NoError(t, svc.DB.Create(ev).Error)
		return ev.ID
	}

	const goroutines = 2
	type args struct {
		req     *models.Request
		eventID uint
	}
	prepared := make([]args, goroutines)
	for i := range prepared {
		prepared[i] = args{req: newRequest(), eventID: newEvent()}
	}

	responses := make(chan *models.Response, goroutines)
	ready := make(chan struct{})
	var wg sync.WaitGroup

	for i := range goroutines {
		wg.Add(1)
		a := prepared[i]
		go func() {
			defer wg.Done()
			<-ready
			controller.HandleCreateJITWalletEvent(ctx, a.req, a.eventID, hub,
				func(r *models.Response, _ nostr.Tags) { responses <- r })
		}()
	}
	close(ready)
	wg.Wait()
	close(responses)

	var successes, lockRejections int
	for r := range responses {
		if r.Error == nil {
			successes++
		} else {
			assert.Equal(t, constants.ERROR_INTERNAL, r.Error.Code)
			assert.Contains(t, r.Error.Message, "already in progress")
			lockRejections++
		}
	}
	assert.Equal(t, 1, successes, "exactly one concurrent create_jit_wallet should succeed")
	assert.Equal(t, 1, lockRejections, "the other must be rejected by the hub creation lock while the winner is still in flight")

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps)
	assert.Len(t, childApps, 1, "exactly one jit_wallet child must exist after the race")
}
