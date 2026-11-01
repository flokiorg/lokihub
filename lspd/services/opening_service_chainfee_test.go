package services

import "testing"

// TestIsCurrentChainFeeCheaper_ChecksLiveFeerate is the regression test
// suite for F-18: before this fix, IsCurrentChainFeeCheaper compared two
// entries from the same, rarely-changed fee-settings table and was true
// almost by construction, regardless of real chain-fee movement.
func TestIsCurrentChainFeeCheaper_ChecksLiveFeerate(t *testing.T) {
	svc := &openingService{}

	tests := []struct {
		name       string
		current    *float64
		reference  float64
		wantResult bool
	}{
		{"current below reference is cheaper", floatPtr(10.0), 20.0, true},
		{"current equal to reference is cheaper", floatPtr(20.0), 20.0, true},
		{"current above reference is not cheaper", floatPtr(25.0), 20.0, false},
		{"nil current feerate (estimator failed) is conservatively not cheaper", nil, 20.0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := svc.IsCurrentChainFeeCheaper(tt.current, tt.reference)
			if got != tt.wantResult {
				t.Errorf("IsCurrentChainFeeCheaper(%v, %v) = %v, want %v", tt.current, tt.reference, got, tt.wantResult)
			}
		})
	}
}

func floatPtr(f float64) *float64 { return &f }
