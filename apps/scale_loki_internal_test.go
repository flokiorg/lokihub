package apps

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The loki-to-mloki conversion must saturate rather than wrap.
//
// A budget cap is stored in loki while every amount shown beside it is mloki,
// so it is scaled once on the way out. Multiplying by 1000 is the one place
// that conversion can overflow, and a wrapped cap does not surface as an
// obviously broken number — it surfaces as a NEGATIVE budget, which reads as a
// member who has overspent. Bad administrative data would become a false
// accusation about a real person.
func TestScaleLokiToMloki(t *testing.T) {
	for name, tc := range map[string]struct {
		loki int64
		want int64
	}{
		"zero":                {loki: 0, want: 0},
		"one":                 {loki: 1, want: 1000},
		"ordinary cap":        {loki: 500, want: 500_000},
		"largest exact":       {loki: math.MaxInt64 / 1000, want: (math.MaxInt64 / 1000) * 1000},
		"one past the edge":   {loki: math.MaxInt64/1000 + 1, want: math.MaxInt64},
		"absurdly large":      {loki: math.MaxInt64, want: math.MaxInt64},
		"negative one":        {loki: -1, want: -1000},
		"absurdly negative":   {loki: math.MinInt64, want: math.MinInt64},
		"one past the bottom": {loki: math.MinInt64/1000 - 1, want: math.MinInt64},
	} {
		t.Run(name, func(t *testing.T) {
			got := scaleLokiToMloki(tc.loki)
			assert.Equal(t, tc.want, got)
			// The property that actually matters: a positive cap can never
			// come out negative, whatever was stored.
			if tc.loki > 0 {
				assert.Positive(t, got, "a positive cap must never wrap to a negative budget")
			}
		})
	}
}

// Every expiry-bucket boundary, including the two the query currently makes
// unreachable.
//
// The boundaries are all strict "less than", so a bill expiring in exactly
// 24h belongs to the 7d bucket, not the 24h one. That is consistent but easy
// to get wrong by a second in either direction, and the figures sit directly
// under a total they must partition exactly.
//
// The negative case is the one worth having: it cannot occur today because
// the outstanding query excludes deadlines already past, but without the
// guard a negative duration falls through to the first "less than" arm and
// files already-expired value under "expires within a day" — the opposite of
// true. This is what stops a future edit to that WHERE clause introducing it
// silently.
func TestExpiryBucketKey(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time {
		t := now.Add(d)
		return &t
	}

	for name, tc := range map[string]struct {
		expiresAt *time.Time
		want      string
	}{
		"no deadline":            {expiresAt: nil, want: "never"},
		"already past":           {expiresAt: at(-time.Hour), want: "24h"},
		"one second past":        {expiresAt: at(-time.Second), want: "24h"},
		"right now":              {expiresAt: at(0), want: "24h"},
		"in an hour":             {expiresAt: at(time.Hour), want: "24h"},
		"one second under a day": {expiresAt: at(24*time.Hour - time.Second), want: "24h"},
		"exactly a day":          {expiresAt: at(24 * time.Hour), want: "7d"},
		"one second under a week": {
			expiresAt: at(7*24*time.Hour - time.Second), want: "7d",
		},
		"exactly a week":           {expiresAt: at(7 * 24 * time.Hour), want: "30d"},
		"one second under a month": {expiresAt: at(30*24*time.Hour - time.Second), want: "30d"},
		"exactly a month":          {expiresAt: at(30 * 24 * time.Hour), want: "later"},
		"a year out":               {expiresAt: at(365 * 24 * time.Hour), want: "later"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, expiryBucketKey(tc.expiresAt, now))
		})
	}
}
