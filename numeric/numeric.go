// Package numeric holds the lossless-conversion helpers for caller-supplied
// integers.
package numeric

import "math"

// ClampUint64ToInt converts v to an int, clamping to math.MaxInt instead of
// silently wrapping to a negative number when v exceeds what int can hold.
// Intended for pagination params (offset/limit) that reach APIs typed as
// int (e.g. gorm's Offset/Limit), so an oversized caller-supplied value
// can't reinterpret as negative and be treated as "no limit" downstream.
func ClampUint64ToInt(v uint64) int {
	if v > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(v)
}

// ClampUint64ToInt64 converts v to an int64, clamping to math.MaxInt64
// instead of silently wrapping to a negative number when v exceeds what
// int64 can hold. Intended for caller-supplied uint64 values (e.g. Unix
// timestamps) that flow into APIs typed as int64.
func ClampUint64ToInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}
