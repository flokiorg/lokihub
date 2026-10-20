package nip47

import (
	"sort"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"
)

const (
	// timingSkewSamples is how many interleaved pairs one round measures.
	timingSkewSamples = 500
	// timingSkewRounds is how many rounds a timing test may take to pass. Load on a
	// shared runner only adds noise, so a real leak fails every round while noise
	// rarely fails three in a row.
	timingSkewRounds = 3
)

// timingSkew is one round's comparison of two probes.
type timingSkew struct {
	aMed, bMed, delta time.Duration
	// ratio is |delta| over the faster arm's median.
	ratio float64
}

// measureTimingSkew interleaves samples pairs of a and b and compares them by the
// median of the per-pair differences, which cancels drift that hits both probes
// of a pair alike (GC, scheduler, a busy neighbour).
func measureTimingSkew(samples int, a, b func() time.Duration) timingSkew {
	as := make([]time.Duration, 0, samples)
	bs := make([]time.Duration, 0, samples)
	diffs := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		x, y := a(), b()
		as, bs, diffs = append(as, x), append(bs, y), append(diffs, x-y)
	}
	s := timingSkew{aMed: median(as), bMed: median(bs), delta: median(diffs)}
	if s.delta < 0 {
		s.delta = -s.delta
	}
	s.ratio = float64(s.delta) / float64(max(min(s.aMed, s.bMed), 1))
	return s
}

// lowestTimingSkew runs up to rounds rounds and returns the first under bound, or
// the lowest seen. Each round is logged.
func lowestTimingSkew(t *testing.T, bound float64, a, b func() time.Duration) timingSkew {
	t.Helper()
	var best timingSkew
	for round := 1; round <= timingSkewRounds; round++ {
		s := measureTimingSkew(timingSkewSamples, a, b)
		t.Logf("round %d: medians %s vs %s, paired skew %s (%.1f%% of the faster arm)",
			round, s.aMed, s.bMed, s.delta, s.ratio*100)
		if round == 1 || s.ratio < best.ratio {
			best = s
		}
		if s.ratio < bound {
			break
		}
	}
	return best
}

func median(ds []time.Duration) time.Duration {
	c := append([]time.Duration(nil), ds...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[len(c)/2]
}

// The paired, best-of-rounds measurement must still catch the defect the timing
// tests guard against: one path paying for a signature verification the other
// skips, which measured as ~97% skew. Here the shared work is itself one
// verification, so the leaking arm costs about twice the other.
func TestTimingSkew_OneSidedSignatureCheckFailsEveryRound(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("wall-clock skew is meaningless under the race detector")
	}
	ev := nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "timing"}
	require.NoError(t, ev.Sign(nostr.GeneratePrivateKey()))
	verify := func() {
		ok, err := ev.CheckSignature()
		require.NoError(t, err)
		require.True(t, ok)
	}
	timed := func(work func()) time.Duration {
		start := time.Now()
		work()
		return time.Since(start)
	}
	leaking := func() time.Duration { return timed(func() { verify(); verify() }) }
	baseline := func() time.Duration { return timed(verify) }

	for round := 1; round <= timingSkewRounds; round++ {
		s := measureTimingSkew(timingSkewSamples, leaking, baseline)
		t.Logf("round %d: paired skew %.1f%%", round, s.ratio*100)
		require.Greater(t, s.ratio, 0.25, "round %d missed a one-sided signature check", round)
	}
}
