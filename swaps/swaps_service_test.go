package swaps

import (
	"sync"
	"testing"
	"time"

	"github.com/lightzapp/lightz-client/pkg/lightz"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// ---------- test double ----------

type mockLightzWebsocket struct {
	mu           sync.Mutex
	updatesCh    chan lightz.SwapUpdate
	subscribed   []string
	connectErr   error
	subscribeErr error
	connectHook  func() // called at the start of Connect(), used to synchronize tests
}

func newMockWs() *mockLightzWebsocket {
	return &mockLightzWebsocket{
		updatesCh: make(chan lightz.SwapUpdate, 8),
	}
}

func (m *mockLightzWebsocket) Connect() error {
	if m.connectHook != nil {
		m.connectHook()
	}
	return m.connectErr
}
func (m *mockLightzWebsocket) Close() error      { close(m.updatesCh); return nil }
func (m *mockLightzWebsocket) Connected() bool   { return true }
func (m *mockLightzWebsocket) Reconnect() error  { return nil }
func (m *mockLightzWebsocket) Unsubscribe(_ string) {}
func (m *mockLightzWebsocket) UpdatesChan() <-chan lightz.SwapUpdate { return m.updatesCh }
func (m *mockLightzWebsocket) Subscribe(ids []string) error {
	m.mu.Lock()
	m.subscribed = append(m.subscribed, ids...)
	m.mu.Unlock()
	return m.subscribeErr
}

func (m *mockLightzWebsocket) getSubscribed() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.subscribed))
	copy(out, m.subscribed)
	return out
}

// ---------- helpers ----------

func newTestSwapsService(ws lightzWebsocket) *swapsService {
	return &swapsService{
		lightzWs:      ws,
		swapListeners: make(map[string]chan lightz.SwapUpdate),
		logger:        zerolog.Nop(),
	}
}

// ---------- tests ----------

func TestRunConnectAndDispatch_ResubscribesActiveSwaps(t *testing.T) {
	ws := newMockWs()
	svc := newTestSwapsService(ws)

	// Pre-register a swap listener as if a swap was in progress before Reload.
	svc.swapListeners["swap-abc"] = make(chan lightz.SwapUpdate, 1)

	go svc.runConnectAndDispatch()

	assert.Eventually(t, func() bool {
		return contains(ws.getSubscribed(), "swap-abc")
	}, time.Second, 10*time.Millisecond, "active swap should be re-subscribed after connect")
}

func TestRunConnectAndDispatch_DispatchesUpdateToListener(t *testing.T) {
	ws := newMockWs()
	svc := newTestSwapsService(ws)

	listenerCh := make(chan lightz.SwapUpdate, 1)
	svc.swapListeners["swap-xyz"] = listenerCh

	go svc.runConnectAndDispatch()

	// Give the goroutine time to reach the dispatch loop.
	time.Sleep(20 * time.Millisecond)

	ws.updatesCh <- lightz.SwapUpdate{Id: "swap-xyz"}

	select {
	case update := <-listenerCh:
		assert.Equal(t, "swap-xyz", update.Id)
	case <-time.After(time.Second):
		t.Fatal("listener channel did not receive the dispatched update")
	}
}

func TestRunConnectAndDispatch_ExitsWhenUpdatesChanClosed(t *testing.T) {
	ws := newMockWs()
	svc := newTestSwapsService(ws)

	done := make(chan struct{})
	go func() {
		svc.runConnectAndDispatch()
		close(done)
	}()

	// Give the goroutine time to reach the dispatch loop, then close the channel.
	time.Sleep(20 * time.Millisecond)
	close(ws.updatesCh)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runConnectAndDispatch did not exit after UpdatesChan was closed")
	}
}

func TestRunConnectAndDispatch_SnapshotsWsOnStart(t *testing.T) {
	ws := newMockWs()
	svc := newTestSwapsService(ws)

	// Connect() is called only after the goroutine has taken its ws snapshot.
	// Closing this channel establishes a happens-before: snapshot read → hook call → nil write.
	connected := make(chan struct{})
	ws.connectHook = func() { close(connected) }

	done := make(chan struct{})
	go func() {
		svc.runConnectAndDispatch()
		close(done)
	}()

	// Wait until the goroutine has called Connect(), meaning the snapshot is safe to race against.
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("goroutine did not call Connect()")
	}

	// Simulate Stop() zeroing the field. The goroutine must continue via its snapshot.
	svc.lightzWs = nil

	// Close the websocket channel so the goroutine exits cleanly.
	close(ws.updatesCh)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runConnectAndDispatch did not exit after channel was closed")
	}
}

func TestRunConnectAndDispatch_NilWsExitsImmediately(t *testing.T) {
	svc := newTestSwapsService(nil)

	done := make(chan struct{})
	go func() {
		svc.runConnectAndDispatch()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runConnectAndDispatch did not exit immediately with nil ws")
	}
}

// ---------- helpers ----------

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
