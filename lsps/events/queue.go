// Package events provides an event queue system for LSPS protocol flow notifications
package events

import (
	"context"
	"sync"
)

// Event is the base interface for all LSPS events
type Event interface {
	EventType() string
}

// EventQueue manages a queue of events
type EventQueue struct {
	events chan Event
	mu     sync.RWMutex
	closed bool
}

// NewEventQueue creates a new event queue
func NewEventQueue(bufferSize int) *EventQueue {
	if bufferSize <= 0 {
		bufferSize = 100
	}
	return &EventQueue{
		events: make(chan Event, bufferSize),
	}
}

// Enqueue adds an event to the queue
func (eq *EventQueue) Enqueue(event Event) {
	eq.mu.RLock()
	defer eq.mu.RUnlock()

	if eq.closed {
		return
	}

	select {
	case eq.events <- event:
	default:
		// Queue is full, drop the event (or could log a warning)
	}
}

// NextEvent blocks until the next event is available or context is cancelled
func (eq *EventQueue) NextEvent(ctx context.Context) (Event, error) {
	select {
	case event, ok := <-eq.events:
		if !ok {
			return nil, context.Canceled
		}
		return event, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// GetAndClearPendingEvents returns all pending events without blocking
func (eq *EventQueue) GetAndClearPendingEvents() []Event {
	events := []Event{}
	for {
		select {
		case event, ok := <-eq.events:
			if !ok {
				return events
			}
			events = append(events, event)
		default:
			return events
		}
	}
}

// Close closes the event queue
func (eq *EventQueue) Close() {
	eq.mu.Lock()
	defer eq.mu.Unlock()

	if !eq.closed {
		eq.closed = true
		close(eq.events)
	}
}
