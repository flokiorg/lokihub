package service

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// Part 3 of data/docs/design/subwallet-scale-benchmark-and-redesign-2026-09-25.md:
// the candidate structures for the wallet registry, benched head to head before
// one is chosen. Bench-only on purpose — nothing here is wired into the service
// until the numbers pick a winner.
//
// The target is a million wallets per HUB, and the registry is per process, so a
// node running three such hubs holds 3M. Hence the top rung.
//
// What the current structure does (measured in wallet_registry_bench_test.go):
// reads are excellent — ~10 ns, flat, allocation-free — and every write copies
// the entire map: 242 ms and 56 MB at 1M. Only writes and resident memory are in
// question.

// candidateLadder is deliberately just the two rungs that decide anything. The
// shape is already established by the main ladder.
var candidateLadder = []int{1_000_000, 3_000_000}

// registryCandidate is the surface acceptsRequestEvent needs.
//
// fill exists so building a fixture of N is not itself O(N^2): filling a
// copy-on-write structure one key at a time copies the whole set per key, which
// at 3M would never finish. Every candidate fills in bulk the way startup does.
type registryCandidate interface {
	add(pubkey string)
	fill(pubkeys []string)
	remove(pubkey string)
	has(pubkey string) bool
}

// --- candidate 1: today's structure, for reference ---------------------------

type copyOnWrite struct{ inner *walletRegistry }

func newCopyOnWrite() registryCandidate   { return &copyOnWrite{inner: newWalletRegistry()} }
func (c *copyOnWrite) add(pk string)      { c.inner.Add(pk) }
func (c *copyOnWrite) fill(pks []string)  { c.inner.Add(pks...) }
func (c *copyOnWrite) remove(pk string)   { c.inner.Remove(pk) }
func (c *copyOnWrite) has(pk string) bool { return c.inner.Has(pk) }

// --- candidate 2: sharded copy-on-write --------------------------------------
//
// Keeps the lock-free read property exactly, and a write copies 1/shards of the
// set instead of all of it.

const candidateShards = 256

type shardedCoW struct {
	shards [candidateShards]atomic.Pointer[map[string]struct{}]
}

func newShardedCoW() registryCandidate {
	s := &shardedCoW{}
	for i := range s.shards {
		empty := map[string]struct{}{}
		s.shards[i].Store(&empty)
	}
	return s
}

// shardOf keys on the first byte of the hex pubkey. Pubkeys are uniform, so the
// first two hex characters spread evenly over 256 buckets without hashing.
func shardOf(pubkey string) int {
	if len(pubkey) < 2 {
		return 0
	}
	return int(unhexNibble(pubkey[0]))<<4 | int(unhexNibble(pubkey[1]))
}

func unhexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

func (s *shardedCoW) add(pk string) {
	shard := &s.shards[shardOf(pk)]
	for {
		current := shard.Load()
		if _, exists := (*current)[pk]; exists {
			return
		}
		next := make(map[string]struct{}, len(*current)+1)
		for k := range *current {
			next[k] = struct{}{}
		}
		next[pk] = struct{}{}
		if shard.CompareAndSwap(current, &next) {
			return
		}
	}
}

func (s *shardedCoW) fill(pks []string) {
	buckets := make([][]string, candidateShards)
	for _, pk := range pks {
		i := shardOf(pk)
		buckets[i] = append(buckets[i], pk)
	}
	for i, bucket := range buckets {
		if len(bucket) == 0 {
			continue
		}
		next := make(map[string]struct{}, len(bucket))
		for _, pk := range bucket {
			next[pk] = struct{}{}
		}
		s.shards[i].Store(&next)
	}
}

func (s *shardedCoW) remove(pk string) {
	shard := &s.shards[shardOf(pk)]
	for {
		current := shard.Load()
		if _, exists := (*current)[pk]; !exists {
			return
		}
		next := make(map[string]struct{}, len(*current))
		for k := range *current {
			if k != pk {
				next[k] = struct{}{}
			}
		}
		if shard.CompareAndSwap(current, &next) {
			return
		}
	}
}

func (s *shardedCoW) has(pk string) bool {
	_, ok := (*s.shards[shardOf(pk)].Load())[pk]
	return ok
}

// --- candidate 3: striped RWMutex over in-place maps -------------------------
//
// Gives up "readers never block behind a write" — but only 1/shards of readers,
// briefly. In exchange a write mutates in place instead of copying anything.

type stripedMutex struct {
	shards [candidateShards]struct {
		mu sync.RWMutex
		m  map[string]struct{}
	}
}

func newStripedMutex() registryCandidate {
	s := &stripedMutex{}
	for i := range s.shards {
		s.shards[i].m = map[string]struct{}{}
	}
	return s
}

func (s *stripedMutex) add(pk string) {
	shard := &s.shards[shardOf(pk)]
	shard.mu.Lock()
	shard.m[pk] = struct{}{}
	shard.mu.Unlock()
}

func (s *stripedMutex) fill(pks []string) {
	for _, pk := range pks {
		s.add(pk)
	}
}

func (s *stripedMutex) remove(pk string) {
	shard := &s.shards[shardOf(pk)]
	shard.mu.Lock()
	delete(shard.m, pk)
	shard.mu.Unlock()
}

func (s *stripedMutex) has(pk string) bool {
	shard := &s.shards[shardOf(pk)]
	shard.mu.RLock()
	_, ok := shard.m[pk]
	shard.mu.RUnlock()
	return ok
}

// --- candidate 4: striped, with raw 32-byte keys -----------------------------
//
// §3.2: the map holds 64-char hex strings, so it pays a string header plus
// separate key data per wallet. A [32]byte key is inline and fixed-size. Costs
// one hex decode per lookup, on the stack.

type stripedRaw struct {
	shards [candidateShards]struct {
		mu sync.RWMutex
		m  map[[32]byte]struct{}
	}
}

func newStripedRaw() registryCandidate {
	s := &stripedRaw{}
	for i := range s.shards {
		s.shards[i].m = map[[32]byte]struct{}{}
	}
	return s
}

// decodeKey parses a hex pubkey into a stack array. Returns false for anything
// malformed, which the gate already rejects on length.
func decodeKey(pubkey string) ([32]byte, bool) {
	var key [32]byte
	if len(pubkey) != 64 {
		return key, false
	}
	if _, err := hex.Decode(key[:], []byte(pubkey)); err != nil {
		return key, false
	}
	return key, true
}

func (s *stripedRaw) add(pk string) {
	key, ok := decodeKey(pk)
	if !ok {
		return
	}
	shard := &s.shards[int(key[0])]
	shard.mu.Lock()
	shard.m[key] = struct{}{}
	shard.mu.Unlock()
}

func (s *stripedRaw) fill(pks []string) {
	for _, pk := range pks {
		s.add(pk)
	}
}

func (s *stripedRaw) remove(pk string) {
	key, ok := decodeKey(pk)
	if !ok {
		return
	}
	shard := &s.shards[int(key[0])]
	shard.mu.Lock()
	delete(shard.m, key)
	shard.mu.Unlock()
}

func (s *stripedRaw) has(pk string) bool {
	key, ok := decodeKey(pk)
	if !ok {
		return false
	}
	shard := &s.shards[int(key[0])]
	shard.mu.RLock()
	_, found := shard.m[key]
	shard.mu.RUnlock()
	return found
}

// --- candidate 5: sync.Map ---------------------------------------------------

type syncMapCandidate struct{ m sync.Map }

func newSyncMap() registryCandidate { return &syncMapCandidate{} }

func (s *syncMapCandidate) add(pk string) { s.m.Store(pk, struct{}{}) }
func (s *syncMapCandidate) fill(pks []string) {
	for _, pk := range pks {
		s.add(pk)
	}
}
func (s *syncMapCandidate) remove(pk string) { s.m.Delete(pk) }
func (s *syncMapCandidate) has(pk string) bool {
	_, ok := s.m.Load(pk)
	return ok
}

// --- candidate 6: a membership filter ----------------------------------------
//
// The registry's only job is rejecting junk before work is spawned, and on a hit
// the handler already does an indexed app lookup. So an approximate filter in
// front of that existing query may be all the memory this needs.
//
// No hashing: a wallet pubkey IS 32 uniform random bytes, so four indices come
// straight out of it. Deletes are the known gap — a plain Bloom filter cannot
// remove, which is why counting variants or periodic rebuilds are the follow-up
// question. Sized here at ~14.4 bits/entry, which is roughly 0.1% false
// positives at capacity.
type bloomCandidate struct {
	bits []uint64
	m    uint64 // bit count, power of two
}

const bloomHashes = 4

func newBloom(capacity int) *bloomCandidate {
	bits := uint64(1)
	want := uint64(capacity) * 15
	for bits < want {
		bits <<= 1
	}
	return &bloomCandidate{bits: make([]uint64, bits/64), m: bits}
}

func (b *bloomCandidate) indices(pk string) ([bloomHashes]uint64, bool) {
	var out [bloomHashes]uint64
	key, ok := decodeKey(pk)
	if !ok {
		return out, false
	}
	for i := 0; i < bloomHashes; i++ {
		out[i] = binary.LittleEndian.Uint64(key[i*8:(i+1)*8]) & (b.m - 1)
	}
	return out, true
}

func (b *bloomCandidate) add(pk string) {
	idx, ok := b.indices(pk)
	if !ok {
		return
	}
	for _, i := range idx {
		b.bits[i/64] |= 1 << (i % 64)
	}
}

func (b *bloomCandidate) fill(pks []string) {
	for _, pk := range pks {
		b.add(pk)
	}
}

// remove is deliberately a no-op: a Bloom filter cannot delete. Benched anyway
// so the write column is honest about what it is not doing.
func (b *bloomCandidate) remove(string) {}

func (b *bloomCandidate) has(pk string) bool {
	idx, ok := b.indices(pk)
	if !ok {
		return false
	}
	for _, i := range idx {
		if b.bits[i/64]&(1<<(i%64)) == 0 {
			return false
		}
	}
	return true
}

// --- fixtures ----------------------------------------------------------------

var (
	candidateKeysOnce sync.Once
	candidateKeys     []string
)

// candidatePubkeys returns n distinct pseudo-random hex pubkeys. Seeded, and
// generated once for the package, since 3M strings is itself a sizeable
// allocation.
func candidatePubkeys(n int) []string {
	candidateKeysOnce.Do(func() {
		total := candidateLadder[len(candidateLadder)-1] + 1000
		candidateKeys = make([]string, total)
		rng := rand.New(rand.NewSource(0x5CA1E)) //nolint:gosec // fixture data
		raw := make([]byte, 32)
		for i := range candidateKeys {
			if _, err := rng.Read(raw); err != nil {
				panic(err)
			}
			candidateKeys[i] = hex.EncodeToString(raw)
		}
	})
	return candidateKeys[:n]
}

type candidateSpec struct {
	name string
	make func(capacity int) registryCandidate
}

var candidates = []candidateSpec{
	{"copyOnWrite", func(int) registryCandidate { return newCopyOnWrite() }},
	{"shardedCoW", func(int) registryCandidate { return newShardedCoW() }},
	{"stripedMutex", func(int) registryCandidate { return newStripedMutex() }},
	{"stripedRaw", func(int) registryCandidate { return newStripedRaw() }},
	{"syncMap", func(int) registryCandidate { return newSyncMap() }},
	{"bloom", func(c int) registryCandidate { return newBloom(c) }},
}

func filledCandidate(spec candidateSpec, n int) registryCandidate {
	c := spec.make(n)
	c.fill(candidatePubkeys(n))
	return c
}

// --- the comparison ----------------------------------------------------------

// BenchmarkCandidateAdd is the write that costs 242 ms today at 1M.
func BenchmarkCandidateAdd(b *testing.B) {
	for _, spec := range candidates {
		for _, n := range candidateLadder {
			b.Run(fmt.Sprintf("%s/n=%d", spec.name, n), func(b *testing.B) {
				c := filledCandidate(spec, n)
				extra := candidatePubkeys(n + b.N)[n:]

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					c.add(extra[i])
				}
			})
		}
	}
}

// BenchmarkCandidateHas is the hot path, run for every event on the relay. It
// must not regress: today it is ~10 ns and allocation-free.
func BenchmarkCandidateHas(b *testing.B) {
	for _, spec := range candidates {
		for _, n := range candidateLadder {
			b.Run(fmt.Sprintf("%s/n=%d", spec.name, n), func(b *testing.B) {
				c := filledCandidate(spec, n)
				probe := candidatePubkeys(n)[n/2]

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if !c.has(probe) {
						b.Fatal("expected a hit")
					}
				}
			})
		}
	}
}

// BenchmarkCandidateHasMiss is the junk-rejection path an attacker drives.
func BenchmarkCandidateHasMiss(b *testing.B) {
	for _, spec := range candidates {
		for _, n := range candidateLadder {
			b.Run(fmt.Sprintf("%s/n=%d", spec.name, n), func(b *testing.B) {
				c := filledCandidate(spec, n)
				probe := candidatePubkeys(n + 1000)[n+500]

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					c.has(probe)
				}
			})
		}
	}
}

// BenchmarkCandidateResident is half the point: memory per wallet held.
func BenchmarkCandidateResident(b *testing.B) {
	for _, spec := range candidates {
		for _, n := range candidateLadder {
			b.Run(fmt.Sprintf("%s/n=%d", spec.name, n), func(b *testing.B) {
				keys := candidatePubkeys(n)

				runtime.GC()
				var before runtime.MemStats
				runtime.ReadMemStats(&before)

				c := spec.make(n)
				c.fill(keys)

				runtime.GC()
				var after runtime.MemStats
				runtime.ReadMemStats(&after)

				resident := int64(after.HeapAlloc) - int64(before.HeapAlloc)
				b.ReportMetric(float64(resident)/float64(n), "B/wallet")
				b.ReportMetric(float64(resident)/(1024*1024), "MB_total")

				if !c.has(keys[n/2]) {
					b.Fatal("structure lost a key")
				}
			})
		}
	}
}

// TestCandidatesAgreeWithTodaysRegistry keeps the comparison honest: an exact
// candidate must answer exactly as the current registry does, and the filter
// must never produce a FALSE NEGATIVE, since that would silently stop serving a
// real wallet.
func TestCandidatesAgreeWithTodaysRegistry(t *testing.T) {
	const n = 20_000
	keys := candidatePubkeys(n + 1000)
	present, absent := keys[:n], keys[n:]

	for _, spec := range candidates {
		t.Run(spec.name, func(t *testing.T) {
			c := spec.make(n)
			c.fill(present)

			for _, pk := range present {
				if !c.has(pk) {
					t.Fatalf("false negative for a registered wallet: %s", pk)
				}
			}

			if spec.name == "bloom" {
				falsePositives := 0
				for _, pk := range absent {
					if c.has(pk) {
						falsePositives++
					}
				}
				// Sized for ~0.1%; assert only that it is not wildly off, since
				// the exact rate is what the design decision turns on.
				if falsePositives > len(absent)/10 {
					t.Fatalf("filter false-positive rate %d/%d is far above its sizing", falsePositives, len(absent))
				}
				t.Logf("filter false positives: %d/%d", falsePositives, len(absent))
				return
			}

			for _, pk := range absent {
				if c.has(pk) {
					t.Fatalf("false positive in an exact structure: %s", pk)
				}
			}

			// Removal must actually remove, which is the property the filter
			// cannot offer.
			c.remove(present[0])
			if c.has(present[0]) {
				t.Fatal("remove did not remove")
			}
		})
	}
}
