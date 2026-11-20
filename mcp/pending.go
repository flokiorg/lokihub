package mcp

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// The residual guard: the irreversible class (on-chain redeem to a foreign
// address, channel open/close) never executes directly from a tool call.
// Instead the call enqueues a PendingAction that the owner must approve
// out-of-band (in the hub, on a surface the assistant cannot drive). Approval
// executes the captured work exactly once. This is the one place revoke-only is
// insufficient — a prompt-injected call here is an unrecoverable loss, so a
// human is kept in the loop for these and only these. See ARCHITECTURE.md §7.

// PendingStatus is the lifecycle state of a queued irreversible action.
type PendingStatus string

const (
	PendingOpen     PendingStatus = "open"
	PendingApproved PendingStatus = "approved"
	PendingDenied   PendingStatus = "denied"
)

const (
	maxOpenPendingPerApp = 5              // approval-flooding guard (audit MED-2)
	pendingTTL           = 24 * time.Hour // an unanswered request lapses
)

var (
	// ErrTooManyPending — the connection already has the maximum number of
	// open requests; the owner must answer some first.
	ErrTooManyPending = errors.New("mcp: too many actions are already waiting for the owner's approval")
	// ErrPendingExpired — the request lapsed before the owner answered.
	ErrPendingExpired = errors.New("mcp: this request expired before it was approved")
	// ErrNoPending — no action with that id.
	ErrNoPending = errors.New("mcp: no such pending action")
	// ErrPendingDenied — the action was denied by the owner.
	ErrPendingDenied = errors.New("mcp: action was denied")
)

// PendingAction is one queued irreversible operation awaiting owner approval.
type PendingAction struct {
	ID      string
	AppID   uint
	Tool    string
	Summary string
	Status  PendingStatus
	Created time.Time

	argsHash [32]byte
	run      func() (any, error)
	once     sync.Once
	result   any
	err      error
	done     bool // run finished (guarded by the store mutex)
}

// PendingStore queues and resolves irreversible actions. Creation is idempotent
// per (appID, tool, key) so a retried guarded call does not stack duplicate
// approvals. In-memory for now; a persistent store can replace it later.
type PendingStore struct {
	mu    sync.Mutex
	byID  map[string]*PendingAction
	byKey map[string]*PendingAction
	seq   int
}

// NewPendingStore builds an empty store.
func NewPendingStore() *PendingStore {
	return &PendingStore{byID: map[string]*PendingAction{}, byKey: map[string]*PendingAction{}}
}

// CreateOrGet returns the existing open action for (appID, tool, key) or creates
// one. The run closure is captured once at first creation. A connection may hold
// at most maxOpenPendingPerApp open requests.
func (s *PendingStore) CreateOrGet(appID uint, tool, key, summary string, run func() (any, error)) (*PendingAction, error) {
	a, _, err := s.CreateOrGetNew(appID, tool, key, [32]byte{}, summary, run)
	return a, err
}

// ErrPendingConflict — a key reused with different arguments.
var ErrPendingConflict = errors.New("mcp: idempotency_key was already used with different arguments")

// CreateOrGetNew is CreateOrGet that also reports whether the record is new and
// binds the key to the arguments (reuse with different args is a conflict).
func (s *PendingStore) CreateOrGetNew(appID uint, tool, key string, args [32]byte, summary string, run func() (any, error)) (*PendingAction, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fmt.Sprintf("%d\x00%s\x00%s", appID, tool, key)
	now := time.Now()
	if a := s.byKey[k]; a != nil && !(a.Status == PendingOpen && now.Sub(a.Created) > pendingTTL) {
		if a.argsHash != args {
			return nil, false, ErrPendingConflict
		}
		return a, false, nil
	}
	s.pruneLocked(now)
	open := 0
	for _, a := range s.byID {
		if a.AppID == appID && a.Status == PendingOpen {
			open++
		}
	}
	if open >= maxOpenPendingPerApp {
		return nil, false, ErrTooManyPending
	}
	s.seq++
	a := &PendingAction{
		ID:       fmt.Sprintf("act-%d-%d", appID, s.seq),
		AppID:    appID,
		Tool:     tool,
		Summary:  summary,
		Status:   PendingOpen,
		Created:  now,
		argsHash: args,
		run:      run,
	}
	s.byID[a.ID] = a
	s.byKey[k] = a
	return a, true, nil
}

// pendingResultFor maps a record to what the assistant is told on a call or
// retry: still waiting, executed (with its result), or refused.
type pendingResult struct {
	Status   string `json:"status"`
	ActionID string `json:"action_id"`
	Summary  string `json:"summary"`
	Result   any    `json:"result,omitempty"` // set when Status == "executed"
}

// Result reports an action's current state to the assistant (audit N5).
func (s *PendingStore) Result(id string) pendingResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.byID[id]
	if a == nil {
		return pendingResult{Status: "expired", ActionID: id}
	}
	r := pendingResult{ActionID: a.ID, Summary: a.Summary}
	switch a.Status {
	case PendingApproved:
		switch {
		case !a.done:
			r.Status = "executing"
		case a.err != nil:
			r.Status = "failed"
		default:
			r.Status, r.Result = "executed", a.result
		}
	case PendingDenied:
		r.Status = "denied"
	default:
		r.Status = "pending_approval"
	}
	return r
}

// pruneLocked drops lapsed open requests and decided requests older than the
// TTL, so the store stays bounded.
func (s *PendingStore) pruneLocked(now time.Time) {
	for id, a := range s.byID {
		if now.Sub(a.Created) > pendingTTL {
			delete(s.byID, id)
			for k, v := range s.byKey {
				if v == a {
					delete(s.byKey, k)
				}
			}
		}
	}
}

// DropForApp removes every open request of a connection (called when the owner
// revokes it, so nothing it asked for can be approved later).
func (s *PendingStore) DropForApp(appID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, a := range s.byID {
		if a.AppID == appID && a.Status == PendingOpen {
			a.Status = PendingDenied
			delete(s.byID, id)
			for k, v := range s.byKey {
				if v == a {
					delete(s.byKey, k)
				}
			}
		}
	}
}

// PendingSnapshot is a lock-free copy of a pending action for listing.
type PendingSnapshot struct {
	ID      string
	AppID   uint
	Tool    string
	Summary string
	Status  PendingStatus
	Created time.Time
}

func (a *PendingAction) snapshot() PendingSnapshot {
	return PendingSnapshot{ID: a.ID, AppID: a.AppID, Tool: a.Tool, Summary: a.Summary, Status: a.Status, Created: a.Created}
}

// Get returns an action by id.
func (s *PendingStore) Get(id string) (*PendingAction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[id]
	return a, ok
}

// List returns snapshots of a connection's actions.
func (s *PendingStore) List(appID uint) []PendingSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PendingSnapshot
	for _, a := range s.byID {
		if a.AppID == appID {
			out = append(out, a.snapshot())
		}
	}
	return out
}

// Approve executes the action's work exactly once and returns its result. A
// second approval returns the same cached result without re-executing. A denied
// action cannot be approved.
func (s *PendingStore) Approve(id string) (any, error) {
	s.mu.Lock()
	a := s.byID[id]
	if a == nil {
		s.mu.Unlock()
		return nil, ErrNoPending
	}
	if a.Status == PendingDenied {
		s.mu.Unlock()
		return nil, ErrPendingDenied
	}
	if a.Status == PendingOpen && time.Since(a.Created) > pendingTTL {
		s.mu.Unlock()
		return nil, ErrPendingExpired
	}
	a.Status = PendingApproved // under the lock: no race with List/Deny
	s.mu.Unlock()
	a.once.Do(func() {
		res, err := a.run()
		s.mu.Lock()
		a.result, a.err, a.done = res, err, true
		s.mu.Unlock()
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	return a.result, a.err
}

// Deny marks an open action denied so it can never execute. Approving or denying
// an already-approved action does not undo the execution.
func (s *PendingStore) Deny(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.byID[id]
	if a == nil {
		return ErrNoPending
	}
	if a.Status == PendingOpen {
		a.Status = PendingDenied
	}
	return nil
}

// ListAll returns snapshots of every queued action (the owner's queue), taken
// under the lock (audit LOW-1: no unsynchronised reads of Status).
func (s *PendingStore) ListAll() []PendingSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	out := make([]PendingSnapshot, 0, len(s.byID))
	for _, a := range s.byID {
		out = append(out, a.snapshot())
	}
	return out
}
