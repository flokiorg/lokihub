package events

import (
	"context"
	"testing"
	"time"
)

// Mock event
type testEvent struct {
	id string
}

func (e *testEvent) EventType() string {
	return "test_event"
}

func TestEventQueue_Enqueue(t *testing.T) {
	queue := NewEventQueue(10)
	defer queue.Close()

	event := &testEvent{id: "test1"}
	queue.Enqueue(event)

	// Should be able to retrieve event
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	received, err := queue.NextEvent(ctx)
	if err != nil {
		t.Fatalf("NextEvent failed: %v", err)
	}

	if received == nil {
		t.Fatal("Expected event, got nil")
	}

	testEv, ok := received.(*testEvent)
	if !ok {
		t.Fatal("Event type mismatch")
	}

	if testEv.id != "test1" {
		t.Errorf("Expected event id test1, got %s", testEv.id)
	}
}

func TestEventQueue_Multiple(t *testing.T) {
	queue := NewEventQueue(10)
	defer queue.Close()

	// Enqueue multiple events
	for i := 0; i < 5; i++ {
		queue.Enqueue(&testEvent{id: string(rune('a' + i))})
	}

	// Retrieve all events
	events := queue.GetAndClearPendingEvents()

	if len(events) != 5 {
		t.Fatalf("Expected 5 events, got %d", len(events))
	}

	for i, event := range events {
		testEv, ok := event.(*testEvent)
		if !ok {
			t.Fatal("Event type mismatch")
		}
		expectedID := string(rune('a' + i))
		if testEv.id != expectedID {
			t.Errorf("Expected event id %s, got %s", expectedID, testEv.id)
		}
	}
}

func TestEventQueue_ContextCancellation(t *testing.T) {
	queue := NewEventQueue(10)
	defer queue.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := queue.NextEvent(ctx)
	if err == nil {
		t.Fatal("Expected context cancellation error")
	}
}

func TestEventQueue_BufferFull(t *testing.T) {
	queue := NewEventQueue(2)
	defer queue.Close()

	// Fill buffer
	queue.Enqueue(&testEvent{id: "1"})
	queue.Enqueue(&testEvent{id: "2"})

	// This should be dropped (buffer full)
	queue.Enqueue(&testEvent{id: "3"})

	events := queue.GetAndClearPendingEvents()

	// Should only have 2 events
	if len(events) != 2 {
		t.Fatalf("Expected 2 events, got %d", len(events))
	}
}
