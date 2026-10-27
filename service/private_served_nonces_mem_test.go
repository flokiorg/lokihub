package service

import "sync"

// memServedNonces is an in-memory servedNonces for tests that build a
// privateTransport without a database.
type memServedNonces struct {
	mu sync.Mutex
	m  map[string]int64
}

func newMemServedNonces() *memServedNonces { return &memServedNonces{m: map[string]int64{}} }

func (s *memServedNonces) Seen(nonce string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[nonce]
	return ok
}

func (s *memServedNonces) MarkServed(nonce string, notAfter int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[nonce]; ok {
		return false
	}
	s.m[nonce] = notAfter
	return true
}

func (s *memServedNonces) Len() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.m))
}
