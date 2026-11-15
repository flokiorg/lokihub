package tests

import (
	"context"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/events"
)

type mockEventConsumer struct {
	mu             sync.Mutex
	consumedEvents []*events.Event
}

func NewMockEventConsumer() *mockEventConsumer {
	return &mockEventConsumer{
		consumedEvents: []*events.Event{},
	}
}

func (e *mockEventConsumer) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.consumedEvents = append(e.consumedEvents, event)
}

func (e *mockEventConsumer) GetConsumedEvents() []*events.Event {
	// events.Publish (as opposed to PublishSync) dispatches to subscribers on
	// their own goroutine, so give it a bit of time to land before reading -
	// the mutex below makes the read/write itself race-safe, this sleep is
	// only about ordering, not memory safety.
	time.Sleep(10 * time.Millisecond)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.consumedEvents
}
