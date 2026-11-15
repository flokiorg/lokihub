package interceptor

import "testing"

// TestScaleForwardAmountMloki is the deterministic regression test for
// F-23: each caller must get an amount computed from ITS OWN incoming
// amount, not a value borrowed from whichever HTLC happened to win a
// shared singleflight race. Intercept calls this fresh, per caller,
// outside that shared closure -- these cases directly prove the formula
// used is correct for both the single-part and multi-part cases.
func TestScaleForwardAmountMloki(t *testing.T) {
	tests := []struct {
		name                   string
		outgoingAmountMloki    int64
		incomingAmountMloki    int64
		reqIncomingAmountMloki int64
		want                   int64
	}{
		{
			name:                   "single-part payment: req amount equals the shared total, full amount forwarded",
			outgoingAmountMloki:    19_920_000,
			incomingAmountMloki:    20_000_000,
			reqIncomingAmountMloki: 20_000_000,
			want:                   19_920_000,
		},
		{
			name:                   "MPP part at 60% of the shared total forwards 60% of the outgoing amount",
			outgoingAmountMloki:    20_000_000,
			incomingAmountMloki:    20_000_000,
			reqIncomingAmountMloki: 12_000_000,
			want:                   12_000_000,
		},
		{
			name:                   "MPP part at 40% of the shared total forwards 40% of the outgoing amount",
			outgoingAmountMloki:    20_000_000,
			incomingAmountMloki:    20_000_000,
			reqIncomingAmountMloki: 8_000_000,
			want:                   8_000_000,
		},
		{
			name:                   "two different callers against the same shared totals get different, proportional results",
			outgoingAmountMloki:    19_999_000,
			incomingAmountMloki:    12_000_000, // the "winner's" own amount, shared as the denominator
			reqIncomingAmountMloki: 8_000_000,  // a losing caller's own, smaller amount
			want:                   13_332_666, // 19_999_000 * 8_000_000 / 12_000_000, truncated
		},
		{
			name:                   "zero shared denominator is handled without panicking",
			outgoingAmountMloki:    1000,
			incomingAmountMloki:    0,
			reqIncomingAmountMloki: 1000,
			want:                   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scaleForwardAmountMloki(tt.outgoingAmountMloki, tt.incomingAmountMloki, tt.reqIncomingAmountMloki)
			if got != tt.want {
				t.Errorf("scaleForwardAmountMloki(%d, %d, %d) = %d, want %d",
					tt.outgoingAmountMloki, tt.incomingAmountMloki, tt.reqIncomingAmountMloki, got, tt.want)
			}
		})
	}
}

// TestScaleForwardAmountMloki_DifferentCallersGetDifferentAmounts is the
// most direct statement of F-23's fix: given the SAME shared totals (as a
// singleflight-deduped closure would produce), two different callers with
// different req amounts must get different results -- never an identical
// copy of each other's forward amount.
func TestScaleForwardAmountMloki_DifferentCallersGetDifferentAmounts(t *testing.T) {
	const sharedOutgoing = int64(19_999_000)
	const sharedIncoming = int64(20_000_000) // fixed by whichever HTLC won the race

	callerA := scaleForwardAmountMloki(sharedOutgoing, sharedIncoming, 12_000_000)
	callerB := scaleForwardAmountMloki(sharedOutgoing, sharedIncoming, 8_000_000)

	if callerA == callerB {
		t.Fatalf("two callers with different incoming amounts (12M vs 8M) must not receive the same forward amount; both got %d", callerA)
	}

	// 12M is 1.5x 8M -- the forwarded amounts must reflect that ratio.
	ratio := float64(callerA) / float64(callerB)
	if ratio < 1.49 || ratio > 1.51 {
		t.Errorf("expected callerA/callerB ratio ~1.5 (matching their own incoming amounts' ratio), got %.4f", ratio)
	}
}
