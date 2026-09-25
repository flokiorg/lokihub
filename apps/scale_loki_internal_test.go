package apps

import (
	"math"
	"testing"

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
