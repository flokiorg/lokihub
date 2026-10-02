package service

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"testing"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/nip47/models"
)

// Baseline benchmarks for the wallet registry, sized for a hub holding on the
// order of 10^6 sub-wallets (see
// data/docs/design/subwallet-scale-benchmark-and-redesign-2026-09-25.md).
//
// The registry holds EVERY app carrying a wallet pubkey, not just cash bills:
// create_app_consumer derives one unconditionally, so standard connections,
// circle wallets and cash bills all land here. That is why these numbers are
// infrastructure numbers rather than cash ones.
//
// What matters in the output is the SHAPE of each curve across the ladder. A
// flat line is a pass at any absolute cost; a linear one is the finding.

// benchLadder is the N ladder every registry benchmark walks.
var benchLadder = []int{1_000, 10_000, 100_000, 1_000_000}

var (
	benchKeysOnce sync.Once
	// benchKeys holds enough distinct pubkeys for the largest rung plus the
	// headroom a mutating benchmark consumes one-per-iteration.
	benchKeys []string
)

const benchKeyHeadroom = 200_000

// benchPubkeys returns n distinct 64-char hex pubkeys, generated once for the
// whole package: regenerating a million strings per benchmark would dominate
// the measurement it is supposed to support.
//
// Pseudo-random rather than a counter, and that matters for fidelity: real
// wallet pubkeys are uniform over the keyspace, while sequentially generated
// hex shares a long common prefix and lands in contiguous memory, which
// flatters both map hashing and cache behaviour. Seeded, so the set is
// identical from run to run and two baselines stay comparable.
func benchPubkeys(n int) []string {
	benchKeysOnce.Do(func() {
		total := benchLadder[len(benchLadder)-1] + benchKeyHeadroom
		benchKeys = make([]string, total)
		rng := rand.New(rand.NewSource(0xC45)) //nolint:gosec // fixture data, not a key
		raw := make([]byte, 32)
		for i := range benchKeys {
			if _, err := rng.Read(raw); err != nil {
				panic(err)
			}
			benchKeys[i] = hex.EncodeToString(raw)
		}
	})
	return benchKeys[:n]
}

// benchMissingPubkey is a pubkey no rung ever registers, for the miss paths.
func benchMissingPubkey() string {
	return hex.EncodeToString(bytes.Repeat([]byte{0xFF}, 32))
}

// filledRegistry returns a registry preloaded with n pubkeys via one variadic
// Add, which is also how startup loads it.
func filledRegistry(n int) *walletRegistry {
	r := newWalletRegistry()
	r.Add(benchPubkeys(n)...)
	return r
}

// --- Op 1: Add one pubkey into a set of N ------------------------------------
//
// Each iteration adds a DISTINCT key, so the real Add runs its copy rather
// than short-circuiting on the idempotent path. The set therefore grows by
// b.N over the run; keep -benchtime small so that drift stays negligible
// against N.

func BenchmarkRegistryAddOne(b *testing.B) {
	for _, n := range benchLadder {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			r := filledRegistry(n)
			extra := benchPubkeys(n + b.N)[n:]

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.Add(extra[i])
			}
		})
	}
}

// --- Op 2: Add N pubkeys at once (the startup path) --------------------------

func BenchmarkRegistryAddBulk(b *testing.B) {
	for _, n := range benchLadder {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			keys := benchPubkeys(n)

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				r := newWalletRegistry()
				b.StartTimer()
				r.Add(keys...)
			}
		})
	}
}

// --- Op 3: Has, hit and miss (the hot path) ----------------------------------
//
// This runs for every event on the relay, junk included, so it is the one
// number that must not regress under any redesign.

func BenchmarkRegistryHasHit(b *testing.B) {
	for _, n := range benchLadder {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			r := filledRegistry(n)
			probe := benchPubkeys(n)[n/2]

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !r.Has(probe) {
					b.Fatal("expected a hit")
				}
			}
		})
	}
}

func BenchmarkRegistryHasMiss(b *testing.B) {
	for _, n := range benchLadder {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			r := filledRegistry(n)
			probe := benchMissingPubkey()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if r.Has(probe) {
					b.Fatal("expected a miss")
				}
			}
		})
	}
}

// --- Op 4: Remove one pubkey from a set of N --------------------------------

func BenchmarkRegistryRemoveOne(b *testing.B) {
	for _, n := range benchLadder {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			r := filledRegistry(n)
			keys := benchPubkeys(n)
			if b.N > n {
				b.Skipf("needs %d registered keys, have %d", b.N, n)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.Remove(keys[i])
			}
		})
	}
}

// --- Op 5: removing k expired wallets, batched vs one at a time --------------
//
// The prune path. These two are the before/after of the variadic Remove, and
// the gap between them is the whole reason it exists: one copy for the batch
// against k copies for the loop.

// pruneShape is one (registry size, expired count) pair from the prune path.
type pruneShape struct{ n, k int }

// batchShapes covers the realistic sweep range. Cheap either way, since the
// batch pays one copy regardless of k.
var batchShapes = []pruneShape{
	{100_000, 100}, {100_000, 1_000},
	{1_000_000, 100}, {1_000_000, 1_000},
}

// oneByOneShapes deliberately omits {1_000_000, 1_000}. That combination is
// the very thing the variadic Remove fixes — a thousand full copies of a
// million-entry map — and it runs for minutes per iteration, so it is left to
// be extrapolated from {1_000_000, 100} rather than waited on.
var oneByOneShapes = []pruneShape{
	{100_000, 100}, {100_000, 1_000},
	{1_000_000, 100},
}

func BenchmarkRegistryRemoveBatch(b *testing.B) {
	for _, shape := range batchShapes {
		b.Run(fmt.Sprintf("n=%d/k=%d", shape.n, shape.k), func(b *testing.B) {
			keys := benchPubkeys(shape.n)
			doomed := keys[:shape.k]

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				r := filledRegistry(shape.n)
				b.StartTimer()
				r.Remove(doomed...)
			}
		})
	}
}

func BenchmarkRegistryRemoveOneByOne(b *testing.B) {
	for _, shape := range oneByOneShapes {
		b.Run(fmt.Sprintf("n=%d/k=%d", shape.n, shape.k), func(b *testing.B) {
			keys := benchPubkeys(shape.n)
			doomed := keys[:shape.k]

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				r := filledRegistry(shape.n)
				b.StartTimer()
				for _, pk := range doomed {
					r.Remove(pk)
				}
			}
		})
	}
}

// --- Op 6: the gate's reject path across the ladder -------------------------
//
// BenchmarkAcceptsRequestEvent_Rejects already covers this at one wallet;
// this walks it up to a million, since the question is whether the map probe
// stays flat as the registry grows.

func BenchmarkAcceptsRequestEventRejectsAtScale(b *testing.B) {
	for _, n := range benchLadder {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			svc := &service{walletRegistry: filledRegistry(n)}
			event := requestEvent(models.REQUEST_KIND, nostr.Tags{{"p", benchMissingPubkey()}})

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if svc.acceptsRequestEvent(event) {
					b.Fatal("expected a reject")
				}
			}
		})
	}
}

func BenchmarkAcceptsRequestEventAcceptsAtScale(b *testing.B) {
	for _, n := range benchLadder {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			svc := &service{walletRegistry: filledRegistry(n)}
			served := benchPubkeys(n)[n/2]
			event := requestEvent(models.REQUEST_KIND, nostr.Tags{{"p", served}})

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !svc.acceptsRequestEvent(event) {
					b.Fatal("expected an accept")
				}
			}
		})
	}
}

// --- Op 7: resident memory ---------------------------------------------------
//
// Memory is half the goal, and a ns/op-only table hides it. Reported as bytes
// per registered wallet so the rungs are directly comparable.

func BenchmarkRegistryResident(b *testing.B) {
	for _, n := range benchLadder {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			keys := benchPubkeys(n)

			// The keys themselves are generated once for the whole package and
			// are not part of what the registry costs, so they are live before
			// the baseline is taken and excluded from the delta.
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)

			r := newWalletRegistry()
			r.Add(keys...)

			runtime.GC()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)

			resident := int64(after.HeapAlloc) - int64(before.HeapAlloc) //nolint:gosec // G115: a heap delta is meant to be signed, and these figures are nowhere near int64 max
			b.ReportMetric(float64(resident)/float64(n), "B/wallet")
			b.ReportMetric(float64(resident)/(1024*1024), "MB_total")

			// Keep it alive past the second reading.
			if r.Len() != n {
				b.Fatalf("registry holds %d, want %d", r.Len(), n)
			}
		})
	}
}
