package manager

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/flokiorg/lokihub/lsps/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// mockStreamClient controls SubscribeCustomMessages per call index, letting
// tests inject messages, errors, and channel closures on distinct subscriptions.
type mockStreamClient struct {
	mockLNClient
	calls    atomic.Int32
	builders []func() (<-chan lnclient.CustomMessage, <-chan error, error)
}

func (m *mockStreamClient) SubscribeCustomMessages(ctx context.Context) (<-chan lnclient.CustomMessage, <-chan error, error) {
	idx := int(m.calls.Add(1)) - 1
	if idx < len(m.builders) {
		return m.builders[idx]()
	}
	// After all builders exhausted, block until context cancelled.
	msgs := make(chan lnclient.CustomMessage)
	errs := make(chan error)
	go func() {
		<-ctx.Done()
		close(msgs)
		close(errs)
	}()
	return msgs, errs, nil
}

func newStreamTestManager(t *testing.T, client lnclient.LNClient) *LiquidityManager {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s_stream?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&persist.LSP{}, &persist.LSPS1Order{}))

	lspMgr := NewLSPManager(db)
	return &LiquidityManager{
		cfg:             &ManagerConfig{LNClient: client, LSPManager: lspMgr},
		transport:       transport.NewFLNDTransport(client),
		eventQueue:      events.NewEventQueue(10),
		listeners:       make(map[string]chan events.Event),
		unclaimedEvents: make(map[string]events.Event),
		nostrPubkeys:    make(map[string]string),
	}
}

// closedStream returns channels that are immediately closed, simulating a clean
// stream closure with no buffered data.
func closedStream() (<-chan lnclient.CustomMessage, <-chan error, error) {
	msgs := make(chan lnclient.CustomMessage)
	errs := make(chan error)
	close(msgs)
	close(errs)
	return msgs, errs, nil
}

// errorStream sends one error then closes, mimicking a gRPC stream break.
func errorStream(err error) (<-chan lnclient.CustomMessage, <-chan error, error) {
	msgs := make(chan lnclient.CustomMessage, 1)
	errs := make(chan error, 1)
	errs <- err
	close(errs)
	close(msgs)
	return msgs, errs, nil
}

// ---------- drainMessages ----------

func TestDrainMessages_EmptiesBuffer(t *testing.T) {
	m := newStreamTestManager(t, &mockStreamClient{})

	msgs := make(chan lnclient.CustomMessage, 3)
	// Put messages with type 0 (not LSPS type) so dispatchMessage is a no-op.
	msgs <- lnclient.CustomMessage{Type: 0}
	msgs <- lnclient.CustomMessage{Type: 0}
	msgs <- lnclient.CustomMessage{Type: 0}
	close(msgs)

	m.drainMessages(msgs)

	assert.Equal(t, 0, len(msgs))
}

func TestDrainMessages_StopsAtDefault(t *testing.T) {
	m := newStreamTestManager(t, &mockStreamClient{})

	msgs := make(chan lnclient.CustomMessage, 5)
	msgs <- lnclient.CustomMessage{Type: 0}
	// channel is NOT closed and has no more messages — drainMessages must return.

	done := make(chan struct{})
	go func() {
		m.drainMessages(msgs)
		close(done)
	}()

	select {
	case <-done:
		// good — drained the one message and hit the default case
	case <-time.After(500 * time.Millisecond):
		t.Fatal("drainMessages blocked on open channel with no more messages")
	}
}

// ---------- consumeMessageStream ----------

func TestConsumeMessageStream_ReturnsTrueOnStreamClose(t *testing.T) {
	m := newStreamTestManager(t, &mockStreamClient{})

	msgs, errs, _ := closedStream()
	ctx := context.Background()

	resubscribe := m.consumeMessageStream(ctx, msgs, errs)
	assert.True(t, resubscribe, "should signal resubscribe when stream closes cleanly")
}

func TestConsumeMessageStream_ReturnsTrueOnStreamError(t *testing.T) {
	m := newStreamTestManager(t, &mockStreamClient{})

	msgs, errs, _ := errorStream(errors.New("grpc EOF"))
	ctx := context.Background()

	resubscribe := m.consumeMessageStream(ctx, msgs, errs)
	assert.True(t, resubscribe)
}

func TestConsumeMessageStream_ReturnsFalseOnContextCancel(t *testing.T) {
	m := newStreamTestManager(t, &mockStreamClient{})

	msgs := make(chan lnclient.CustomMessage)
	errs := make(chan error)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan bool, 1)
	go func() {
		done <- m.consumeMessageStream(ctx, msgs, errs)
	}()

	cancel()
	select {
	case resubscribe := <-done:
		assert.False(t, resubscribe, "context cancel must not trigger resubscribe")
	case <-time.After(time.Second):
		t.Fatal("consumeMessageStream did not return after context cancel")
	}
}

func TestConsumeMessageStream_DrainsMsgsBeforeReturning(t *testing.T) {
	// Put two messages in the buffer, then close errs first (mimicking
	// the defer order in SubscribeCustomMessages: errChan closes before msgChan).
	msgs := make(chan lnclient.CustomMessage, 5)
	errs := make(chan error, 1)

	m := newStreamTestManager(t, &mockStreamClient{})
	// Intercept dispatchMessage by injecting a handler via the lsps0 client.
	// Since the messages have type 0 (not LSPS), dispatchMessage is a fast no-op,
	// but we can wrap by monkey-patching; instead we just count via msgs buffer size.
	// Simpler: we inject LSPS-type-0 messages and count how many were dequeued.

	// Use a counting mock: override SendCustomMessage to count — but that requires
	// the LSPS protocol flow. Instead, verify via channel drain: after
	// consumeMessageStream returns, the msgs channel must be empty.
	msgs <- lnclient.CustomMessage{Type: 0}
	msgs <- lnclient.CustomMessage{Type: 0}

	errs <- errors.New("boom")
	close(errs)
	// msgs is NOT yet closed; it still has two buffered items.

	ctx := context.Background()
	resubscribe := m.consumeMessageStream(ctx, msgs, errs)

	assert.True(t, resubscribe)
	// After returning, msgs must be empty (drain consumed the two messages).
	assert.Equal(t, 0, len(msgs), "drainMessages should have emptied the buffered msgs channel")
}

// ---------- processMessages ----------

func TestProcessMessages_ReconnectsAfterStreamClose(t *testing.T) {
	var subscribeCount atomic.Int32

	// First subscription closes immediately; second stays open.
	openMsgs := make(chan lnclient.CustomMessage)
	openErrs := make(chan error)

	client := &mockStreamClient{
		builders: []func() (<-chan lnclient.CustomMessage, <-chan error, error){
			func() (<-chan lnclient.CustomMessage, <-chan error, error) {
				subscribeCount.Add(1)
				return closedStream()
			},
			func() (<-chan lnclient.CustomMessage, <-chan error, error) {
				subscribeCount.Add(1)
				return openMsgs, openErrs, nil
			},
		},
	}

	m := newStreamTestManager(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go m.processMessages(ctx)

	// Wait until the second subscription is active (retry delay is streamRetryDelay=5s).
	assert.Eventually(t, func() bool {
		return subscribeCount.Load() >= 2
	}, 12*time.Second, 100*time.Millisecond, "processMessages should resubscribe after stream close")
}

func TestProcessMessages_ReconnectsAfterStreamError(t *testing.T) {
	var subscribeCount atomic.Int32

	openMsgs := make(chan lnclient.CustomMessage)
	openErrs := make(chan error)

	client := &mockStreamClient{
		builders: []func() (<-chan lnclient.CustomMessage, <-chan error, error){
			func() (<-chan lnclient.CustomMessage, <-chan error, error) {
				subscribeCount.Add(1)
				return errorStream(errors.New("grpc stream broken"))
			},
			func() (<-chan lnclient.CustomMessage, <-chan error, error) {
				subscribeCount.Add(1)
				return openMsgs, openErrs, nil
			},
		},
	}

	m := newStreamTestManager(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go m.processMessages(ctx)

	assert.Eventually(t, func() bool {
		return subscribeCount.Load() >= 2
	}, 12*time.Second, 100*time.Millisecond, "processMessages should resubscribe after stream error")
}

func TestProcessMessages_RetriesOnInitialSubscribeFailure(t *testing.T) {
	var subscribeCount atomic.Int32

	openMsgs := make(chan lnclient.CustomMessage)
	openErrs := make(chan error)

	client := &mockStreamClient{}
	client.builders = []func() (<-chan lnclient.CustomMessage, <-chan error, error){
		func() (<-chan lnclient.CustomMessage, <-chan error, error) {
			subscribeCount.Add(1)
			return nil, nil, errors.New("node not ready")
		},
		func() (<-chan lnclient.CustomMessage, <-chan error, error) {
			subscribeCount.Add(1)
			return openMsgs, openErrs, nil
		},
	}

	m := newStreamTestManager(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go m.processMessages(ctx)

	assert.Eventually(t, func() bool {
		return subscribeCount.Load() >= 2
	}, 12*time.Second, 100*time.Millisecond, "processMessages should retry when initial subscribe fails")
}

func TestProcessMessages_StopsOnContextCancel(t *testing.T) {
	// Subscription that blocks forever.
	blockMsgs := make(chan lnclient.CustomMessage)
	blockErrs := make(chan error)

	client := &mockStreamClient{
		builders: []func() (<-chan lnclient.CustomMessage, <-chan error, error){
			func() (<-chan lnclient.CustomMessage, <-chan error, error) {
				return blockMsgs, blockErrs, nil
			},
		},
	}

	m := newStreamTestManager(t, client)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		m.processMessages(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("processMessages did not stop after context cancel")
	}
}
