package notifications

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOpeningStore implements ports.OpeningStore with real logic only for
// GetLsps5Webhooks (all NotificationService needs); every other method is a
// no-op stub so the fake satisfies the (large) interface without pulling in
// a mocking framework for methods this package never calls.
type fakeOpeningStore struct {
	webhooks       map[string][]*core.Lsps5Webhook
	getWebhooksErr error
}

func newFakeOpeningStore() *fakeOpeningStore {
	return &fakeOpeningStore{webhooks: make(map[string][]*core.Lsps5Webhook)}
}

func (f *fakeOpeningStore) GetLsps5Webhooks(clientNodeId string) ([]*core.Lsps5Webhook, error) {
	if f.getWebhooksErr != nil {
		return nil, f.getWebhooksErr
	}
	return f.webhooks[clientNodeId], nil
}

func (f *fakeOpeningStore) GetFeeParamsSettings(string) ([]*core.OpeningFeeParamsSetting, error) {
	return nil, nil
}
func (f *fakeOpeningStore) SetFeeParams(string, time.Duration, core.OpeningFeeParams) error {
	return nil
}
func (f *fakeOpeningStore) RegisterLsps2Buy(*core.Lsps2BuyRegistration) error { return nil }
func (f *fakeOpeningStore) GetLsps2BuyRegistration(uint64) (*core.Lsps2BuyRegistration, error) {
	return nil, nil
}
func (f *fakeOpeningStore) RegisterLsps1Order(*core.Lsps1Order) error      { return nil }
func (f *fakeOpeningStore) GetLsps1Order(string) (*core.Lsps1Order, error) { return nil, nil }
func (f *fakeOpeningStore) GetLsps1OrderByPaymentInvoice(string) (*core.Lsps1Order, error) {
	return nil, nil
}
func (f *fakeOpeningStore) GetPaidLsps1Order(string) (*core.Lsps1Order, error) { return nil, nil }
func (f *fakeOpeningStore) GetPaidLsps1Orders(string) ([]*core.Lsps1Order, error) {
	return nil, nil
}
func (f *fakeOpeningStore) GetAllPaidLsps1Orders() ([]*core.Lsps1Order, error) { return nil, nil }
func (f *fakeOpeningStore) GetAllCreatedLsps1Orders() ([]*core.Lsps1Order, error) {
	return nil, nil
}
func (f *fakeOpeningStore) ListLsps1Orders(int, int) ([]*core.Lsps1Order, error) { return nil, nil }
func (f *fakeOpeningStore) UpdateLsps1OrderState(string, string, string, string) (bool, error) {
	return false, nil
}
func (f *fakeOpeningStore) UpdateLsps1OrderFunding(string, string, int) (bool, error) {
	return true, nil
}
func (f *fakeOpeningStore) RegisterLsps5Webhook(*core.Lsps5Webhook) error { return nil }
func (f *fakeOpeningStore) RemoveLsps5Webhook(string, string) error       { return nil }
func (f *fakeOpeningStore) CountLsps1Orders(string, string, *time.Time) (int, error) {
	return 0, nil
}
func (f *fakeOpeningStore) CountLsps2PendingRegistrations(string) (int, error) { return 0, nil }
func (f *fakeOpeningStore) CountLsps5Webhooks(string) (int, error)             { return 0, nil }
func (f *fakeOpeningStore) GetReservedLiquidity() (uint64, error)              { return 0, nil }
func (f *fakeOpeningStore) ExpireStaleOrders() (int64, error)                  { return 0, nil }
func (f *fakeOpeningStore) ExpireStaleLsps2Registrations() (int64, error)      { return 0, nil }
func (f *fakeOpeningStore) ReserveLsps1Order(*core.Lsps1Order, uint64) (bool, error) {
	return true, nil
}
func (f *fakeOpeningStore) ReserveLsps2Buy(*core.Lsps2BuyRegistration, uint64) (bool, error) {
	return true, nil
}

// fakeNotifStore implements Store in-memory, so processPending/Enqueue tests
// don't need a real database.
type fakeNotifStore struct {
	mu    sync.Mutex
	items map[int64]*NotificationQueueItem
	next  int64

	removeExpiredCalls []time.Time
	removeExpiredErr   error

	registerCalls    []registerCall
	registerErr      error
	unsubscribeCalls []unsubscribeCall
	unsubscribeErr   error
}

func newFakeNotifStore() *fakeNotifStore {
	return &fakeNotifStore{items: make(map[int64]*NotificationQueueItem)}
}

type registerCall struct {
	pubkey, url, transport string
}

type unsubscribeCall struct {
	pubkey, url string
}

func (f *fakeNotifStore) Register(_ context.Context, pubkey, url, transport string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.registerErr != nil {
		return f.registerErr
	}
	f.registerCalls = append(f.registerCalls, registerCall{pubkey, url, transport})
	return nil
}
func (f *fakeNotifStore) GetRegistrations(context.Context, string) ([]core.NotificationRegistration, error) {
	return nil, nil
}
func (f *fakeNotifStore) Unsubscribe(_ context.Context, pubkey, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unsubscribeErr != nil {
		return f.unsubscribeErr
	}
	f.unsubscribeCalls = append(f.unsubscribeCalls, unsubscribeCall{pubkey, url})
	return nil
}

func (f *fakeNotifStore) RemoveExpired(_ context.Context, before time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeExpiredCalls = append(f.removeExpiredCalls, before)
	return f.removeExpiredErr
}

func (f *fakeNotifStore) removeExpiredCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.removeExpiredCalls)
}

func (f *fakeNotifStore) lastRemoveExpiredBefore() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removeExpiredCalls[len(f.removeExpiredCalls)-1]
}

func (f *fakeNotifStore) Enqueue(_ context.Context, pubkey, url, transport, method string, params interface{}) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.items[f.next] = &NotificationQueueItem{
		ID: f.next, Pubkey: pubkey, TargetURL: url, Transport: transport, Method: method, Params: params,
	}
	return f.next, nil
}

func (f *fakeNotifStore) GetPending(_ context.Context, limit int) ([]NotificationQueueItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []NotificationQueueItem
	for _, item := range f.items {
		if len(out) >= limit {
			break
		}
		out = append(out, *item)
	}
	return out, nil
}

func (f *fakeNotifStore) UpdateStatus(_ context.Context, id int64, status string, nextRetry time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if item, ok := f.items[id]; ok {
		item.Attempts++
		_ = status
		_ = nextRetry
		if status != "failed" {
			delete(f.items, id) // simulate leaving the retry window
		}
	}
	return nil
}

// stubTransport lets tests control success/failure per call without a real
// network dependency.
type stubTransport struct {
	transportType TransportType
	sendFunc      func(ctx context.Context, target, method string, params interface{}) error
	calls         int32
	mu            sync.Mutex
}

func (s *stubTransport) Type() TransportType { return s.transportType }
func (s *stubTransport) Send(ctx context.Context, target, method string, params interface{}) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.sendFunc(ctx, target, method, params)
}
func (s *stubTransport) callCount() int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestNotify_DispatchesToAllWebhooksAndDedupsWithinWindow(t *testing.T) {
	store := newFakeOpeningStore()
	store.webhooks["peer1"] = []*core.Lsps5Webhook{
		{ClientNodeId: "peer1", AppName: "wallet-a", Url: "https://a.example", Transport: "push"},
		{ClientNodeId: "peer1", AppName: "wallet-b", Url: "https://b.example", Transport: "push"},
	}

	transport := &stubTransport{
		transportType: TransportTypePush,
		sendFunc:      func(context.Context, string, string, interface{}) error { return nil },
	}

	svc := NewNotificationService(store, nil, newFakeNotifStore())
	svc.AddTransport(transport)

	notified, err := svc.Notify("peer1", "hash1", nil)
	require.NoError(t, err)
	assert.True(t, notified)
	assert.EqualValues(t, 2, transport.callCount(), "should dispatch to both registered webhooks")

	// A second call for the same (pubkey, paymenthash) within the dedup
	// window should short-circuit without hitting the transport again.
	notified, err = svc.Notify("peer1", "hash1", nil)
	require.NoError(t, err)
	assert.True(t, notified)
	assert.EqualValues(t, 2, transport.callCount(), "recent notification should be deduped, not re-dispatched")
}

// TestNotifyAsync_EnqueuesWithoutSynchronousDelivery is P-4's regression
// test: NotifyAsync must durably enqueue for every registered webhook and
// return without ever calling the transport itself -- delivery is the
// background worker's job (processPending), not NotifyAsync's. The
// transport is constructed with a nil sendFunc specifically so any
// synchronous delivery attempt panics instead of silently passing.
func TestNotifyAsync_EnqueuesWithoutSynchronousDelivery(t *testing.T) {
	store := newFakeOpeningStore()
	store.webhooks["peer1"] = []*core.Lsps5Webhook{
		{ClientNodeId: "peer1", AppName: "wallet-a", Url: "https://a.example", Transport: "push"},
		{ClientNodeId: "peer1", AppName: "wallet-b", Url: "https://b.example", Transport: "push"},
	}

	transport := &stubTransport{transportType: TransportTypePush} // nil sendFunc: Send() must never be called
	notifStore := newFakeNotifStore()

	svc := NewNotificationService(store, nil, notifStore)
	svc.AddTransport(transport)

	svc.NotifyAsync("peer1", "hash1", nil)

	assert.EqualValues(t, 0, transport.callCount(), "NotifyAsync must not attempt synchronous delivery")

	pending, err := notifStore.GetPending(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, pending, 2, "one durable queue entry per registered webhook")
	urls := []string{pending[0].TargetURL, pending[1].TargetURL}
	assert.ElementsMatch(t, []string{"https://a.example", "https://b.example"}, urls)
}

// TestNotifyAsync_DedupsWithinWindow mirrors Notify's own dedup coverage:
// a second call for the same (pubkey, paymenthash) within the dedup
// window must not enqueue a second time.
func TestNotifyAsync_DedupsWithinWindow(t *testing.T) {
	store := newFakeOpeningStore()
	store.webhooks["peer1"] = []*core.Lsps5Webhook{
		{ClientNodeId: "peer1", AppName: "wallet-a", Url: "https://a.example", Transport: "push"},
	}
	notifStore := newFakeNotifStore()
	svc := NewNotificationService(store, nil, notifStore)
	svc.AddTransport(&stubTransport{transportType: TransportTypePush})

	svc.NotifyAsync("peer1", "hash1", nil)
	svc.NotifyAsync("peer1", "hash1", nil)

	pending, err := notifStore.GetPending(context.Background(), 10)
	require.NoError(t, err)
	assert.Len(t, pending, 1, "recent notification should be deduped, not re-enqueued")
}

// TestNotifyAsync_NoWebhooksIsNoOp covers the empty-webhooks path: nothing
// to enqueue, and critically no panic despite the empty slice.
func TestNotifyAsync_NoWebhooksIsNoOp(t *testing.T) {
	store := newFakeOpeningStore()
	notifStore := newFakeNotifStore()
	svc := NewNotificationService(store, nil, notifStore)

	svc.NotifyAsync("peer-with-no-webhooks", "hash1", nil)

	pending, err := notifStore.GetPending(context.Background(), 10)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestNotify_NoWebhooksReturnsNotNotified(t *testing.T) {
	store := newFakeOpeningStore()
	svc := NewNotificationService(store, nil, newFakeNotifStore())

	notified, err := svc.Notify("unknown-peer", "hash1", nil)
	require.NoError(t, err)
	assert.False(t, notified)
}

func TestProcessPending_SuccessMarksCompleted(t *testing.T) {
	store := newFakeOpeningStore()
	notifStore := newFakeNotifStore()
	id, err := notifStore.Enqueue(context.Background(), "peer1", "https://a.example", "push", "lsps5.payment_incoming", nil)
	require.NoError(t, err)

	transport := &stubTransport{
		transportType: TransportTypePush,
		sendFunc:      func(context.Context, string, string, interface{}) error { return nil },
	}
	svc := NewNotificationService(store, nil, notifStore)
	svc.AddTransport(transport)

	svc.processPending(context.Background())

	notifStore.mu.Lock()
	_, stillPending := notifStore.items[id]
	notifStore.mu.Unlock()
	assert.False(t, stillPending, "a successfully delivered item should be marked completed and no longer pending")
}

// TestSetNodePrivKey_SetsOnDefaultHTTPSTransport covers a previously
// untested setter: the private key set here is what HTTPSTransport signs
// outgoing lsps5 webhook payloads with.
func TestSetNodePrivKey_SetsOnDefaultHTTPSTransport(t *testing.T) {
	svc := NewNotificationService(newFakeOpeningStore(), nil, newFakeNotifStore())

	svc.SetNodePrivKey("test-priv-key")

	transport, ok := svc.transports[TransportTypePush].(*HTTPSTransport)
	require.True(t, ok, "NewNotificationService must register a default HTTPSTransport under TransportTypePush")
	assert.Equal(t, "test-priv-key", transport.nodePrivKey)
}

// TestStart_StopsPromptlyOnContextCancellation and
// TestRunWorker_StopsPromptlyOnContextCancellation cover Start/runWorker's
// goroutine lifecycles, previously entirely untested. Both loops' real
// intervals (cleanInterval=2m, runWorker's ticker=10s) are too long to
// wait out directly, so these instead prove the ctx.Done() branch of each
// loop's select actually exits promptly -- a regression there (a missing
// or misplaced ctx.Done() case) would hang the service on shutdown
// instead of just eventually ticking again.
func TestStart_StopsPromptlyOnContextCancellation(t *testing.T) {
	svc := NewNotificationService(newFakeOpeningStore(), nil, newFakeNotifStore())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		svc.Start(ctx)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return promptly after context cancellation")
	}
}

func TestRunWorker_StopsPromptlyOnContextCancellation(t *testing.T) {
	svc := NewNotificationService(newFakeOpeningStore(), nil, newFakeNotifStore())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		svc.runWorker(ctx)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWorker did not return promptly after context cancellation")
	}
}

func TestProcessPending_FailureBacksOff(t *testing.T) {
	store := newFakeOpeningStore()
	notifStore := newFakeNotifStore()
	id, err := notifStore.Enqueue(context.Background(), "peer1", "https://a.example", "push", "lsps5.payment_incoming", nil)
	require.NoError(t, err)

	transport := &stubTransport{
		transportType: TransportTypePush,
		sendFunc:      func(context.Context, string, string, interface{}) error { return assert.AnError },
	}
	svc := NewNotificationService(store, nil, notifStore)
	svc.AddTransport(transport)

	svc.processPending(context.Background())

	notifStore.mu.Lock()
	item, stillPending := notifStore.items[id]
	notifStore.mu.Unlock()
	require.True(t, stillPending, "a failed delivery should remain pending for retry")
	assert.EqualValues(t, 1, item.Attempts)
}
