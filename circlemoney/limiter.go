package circlemoney

import (
	"sync"
	"time"
)

// Limiter counts events per wallet in a sliding one-minute window. It is
// process-local, like every rate limit in lokihub.
type Limiter struct {
	mu     sync.Mutex
	events map[uint][]time.Time
}

// NewLimiter returns an empty limiter.
func NewLimiter() *Limiter { return &Limiter{events: map[uint][]time.Time{}} }

// Allow records an event for key at now and reports whether it is within
// perMinute events in the minute before now.
func (l *Limiter) Allow(key uint, perMinute int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-time.Minute)
	kept := l.events[key][:0]
	for _, at := range l.events[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= perMinute {
		l.events[key] = kept
		return false
	}
	l.events[key] = append(kept, now)
	return true
}

// Shared limiters: production builds a controller per request, so the
// counters live at package level.
var (
	// SameCircleTransfers counts each wallet's same-circle transfers.
	SameCircleTransfers = NewLimiter()
	// Invoices counts each wallet's invoices.
	Invoices = NewLimiter()
)
