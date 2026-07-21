package service

// Covers the fix for the NWC-specific race behind orphaned/invisible JIT
// wallets: watchSubscription used to dispatch each incoming NIP-47 event via
// a bare, untracked `go` statement, so StopApp/Shutdown's nostrGroup.Wait()
// could return — and the LN client/DB pool be torn down — while a
// create_jit_wallet request was still mid-flight. Event handling is now
// tracked on the passed-in *errgroup.Group instead, so group.Wait() must
// block until any in-flight handler actually finishes.

import (
	"context"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"golang.org/x/sync/errgroup"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47"
	nostrmodels "github.com/flokiorg/lokihub/nostr/models"
)

// slowHandleEventNip47Service is a minimal Nip47Service fake whose HandleEvent
// signals started immediately, then blocks until unblock is closed before
// signalling ran. Every other interface method is left as a nil embedded
// Nip47Service — watchSubscription only ever calls HandleEvent, so those are
// never invoked.
type slowHandleEventNip47Service struct {
	nip47.Nip47Service
	started chan struct{}
	unblock <-chan struct{}
	ran     chan struct{}
}

func (f *slowHandleEventNip47Service) HandleEvent(ctx context.Context, pool nostrmodels.SimplePool, event *nostr.Event, lnClient lnclient.LNClient) {
	close(f.started)
	<-f.unblock
	close(f.ran)
}

func TestWatchSubscription_GroupWaitsForInFlightEventHandling(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	ran := make(chan struct{})
	svc := &service{
		nip47Service: &slowHandleEventNip47Service{started: started, unblock: unblock, ran: ran},
	}

	ctx, cancel := context.WithCancel(context.Background())
	group := new(errgroup.Group)
	eventsChannel := make(chan nostr.RelayEvent, 1)

	watchDone := make(chan error, 1)
	go func() {
		watchDone <- svc.watchSubscription(ctx, nil, eventsChannel, "wallet-pubkey", group)
	}()

	// Deliver one event, wait for the handler to actually start (avoiding a
	// race where cancel() below wins the inner select before the event is
	// even dispatched), then cancel — simulating a shutdown/lock/reload
	// arriving while a NIP-47 request (e.g. create_jit_wallet, mid
	// fund-transfer) is still being handled.
	eventsChannel <- nostr.RelayEvent{Event: &nostr.Event{}}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler was never dispatched")
	}
	cancel()

	// watchSubscription itself must return promptly on ctx.Done() — it does
	// not wait for dispatched handlers itself, group.Wait() does.
	select {
	case <-watchDone:
	case <-time.After(time.Second):
		t.Fatal("watchSubscription did not return after ctx cancellation")
	}

	// group.Wait() must block until the slow handler finishes: assert it
	// hasn't finished yet, then unblock it and confirm Wait() only resolves
	// after that.
	waitErrCh := make(chan error, 1)
	go func() {
		waitErrCh <- group.Wait()
	}()

	select {
	case <-ran:
		t.Fatal("handler must not have completed yet — group.Wait() raced ahead of it")
	case <-time.After(100 * time.Millisecond):
		// expected: still blocked
	}

	close(unblock)

	select {
	case err := <-waitErrCh:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("group.Wait() did not return after the in-flight handler finished")
	}
	select {
	case <-ran:
	default:
		t.Fatal("handler must have run before group.Wait() returned")
	}
}
