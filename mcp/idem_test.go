package mcp

import (
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// TestIdemStoreConcurrent is the core of M-REPLAY-1: many concurrent calls with
// the same (appID, tool, key) must execute the underlying work exactly once and
// all observe the same result; different keys execute independently.
func TestIdemStoreConcurrent(t *testing.T) {
	s := NewIdemStore()
	h := ArgsHash("args")
	var runs int32

	const workers = 50
	var wg sync.WaitGroup
	results := make([]any, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, _ := s.Do(1, "send_payment", "same-key", h, func() (any, error) {
				atomic.AddInt32(&runs, 1)
				return "preimage", nil
			})
			results[i] = res
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("same key executed %d times, want exactly 1 (double-spend!)", got)
	}
	for i, r := range results {
		if r != "preimage" {
			t.Errorf("worker %d got %v, want cached 'preimage'", i, r)
		}
	}

	// Different keys run independently.
	_, _ = s.Do(1, "send_payment", "k-a", h, func() (any, error) { atomic.AddInt32(&runs, 1); return nil, nil })
	_, _ = s.Do(1, "send_payment", "k-b", h, func() (any, error) { atomic.AddInt32(&runs, 1); return nil, nil })
	if got := atomic.LoadInt32(&runs); got != 3 {
		t.Fatalf("after two new keys, runs = %d, want 3", got)
	}

	// Same key on a different tool, and the same key for a different connection,
	// are both distinct (no cross-tool / cross-connection collision).
	_, _ = s.Do(1, "other_tool", "same-key", h, func() (any, error) { atomic.AddInt32(&runs, 1); return nil, nil })
	_, _ = s.Do(2, "send_payment", "same-key", h, func() (any, error) { atomic.AddInt32(&runs, 1); return nil, nil })
	if got := atomic.LoadInt32(&runs); got != 5 {
		t.Fatalf("cross-tool/cross-conn keys collided: runs = %d, want 5", got)
	}
}

// TestIdemConflictAndNoErrorCache (audit LOW-3): a key reused with different
// args is refused (never a stale "success"); a failed run is not cached, so the
// same key can retry; the store is size-bounded (audit MED-2).
func TestIdemConflictAndNoErrorCache(t *testing.T) {
	s := NewIdemStore()
	a, b := ArgsHash(map[string]int{"amount": 1}), ArgsHash(map[string]int{"amount": 2})
	if _, err := s.Do(1, "send_payment", "k", a, func() (any, error) { return "paid-1", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Do(1, "send_payment", "k", b, func() (any, error) { return "paid-2", nil }); !errors.Is(err, ErrIdempotencyConflict) {
		t.Errorf("reused key with different args err = %v, want conflict", err)
	}

	var runs int32
	fail := func() (any, error) { atomic.AddInt32(&runs, 1); return nil, errors.New("transient") }
	ok := func() (any, error) { atomic.AddInt32(&runs, 1); return "done", nil }
	if _, err := s.Do(1, "t", "retry", a, fail); err == nil {
		t.Fatal("expected failure")
	}
	if res, err := s.Do(1, "t", "retry", a, ok); err != nil || res != "done" || atomic.LoadInt32(&runs) != 2 {
		t.Errorf("retry after failure: res=%v err=%v runs=%d", res, err, runs)
	}

	for i := 0; i < idemMaxEntries+50; i++ {
		_, _ = s.Do(9, "t", strconv.Itoa(i), a, func() (any, error) { return nil, nil })
	}
	if n := s.size(); n > idemMaxEntries {
		t.Errorf("idempotency store grew to %d (> cap %d)", n, idemMaxEntries)
	}
}

// TestIdemPerAppIsolation (re-audit N3): one connection flooding new keys can
// never evict another connection's settled record (which would re-open a
// retry double-execution for it), and in-flight entries are never evicted.
func TestIdemPerAppIsolation(t *testing.T) {
	s := NewIdemStore()
	h := ArgsHash("x")
	var runs int32
	pay := func() (any, error) { atomic.AddInt32(&runs, 1); return "paid", nil }
	if _, err := s.Do(1, "send_payment", "victim-key", h, pay); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < idemMaxPerApp*3; i++ {
		_, _ = s.Do(2, "set_node_alias", strconv.Itoa(i), h, func() (any, error) { return nil, nil })
	}
	// The victim's retry must hit its cached record, not run again.
	if _, err := s.Do(1, "send_payment", "victim-key", h, pay); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("flood by another connection evicted the record: executions = %d", got)
	}

	// An in-flight entry survives eviction pressure from its own connection.
	release := make(chan struct{})
	started := make(chan struct{})
	var slow int32
	go func() {
		_, _ = s.Do(3, "send_payment", "slow", h, func() (any, error) {
			close(started)
			<-release
			atomic.AddInt32(&slow, 1)
			return "ok", nil
		})
	}()
	<-started
	for i := 0; i < idemMaxPerApp+10; i++ {
		_, _ = s.Do(3, "t", strconv.Itoa(i), h, func() (any, error) { return nil, nil })
	}
	done := make(chan struct{})
	go func() {
		_, _ = s.Do(3, "send_payment", "slow", h, func() (any, error) { atomic.AddInt32(&slow, 1); return "ok", nil })
		close(done)
	}()
	close(release)
	<-done
	if got := atomic.LoadInt32(&slow); got != 1 {
		t.Fatalf("in-flight entry was evicted and re-executed: %d", got)
	}
}
