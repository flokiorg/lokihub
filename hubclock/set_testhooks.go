//go:build testhooks

package hubclock

// SetOffset moves the Hub's clock offsetSecs ahead of the wall clock (0 resets
// it). Only testhooks builds can call it.
func SetOffset(offsetSecs int64) { setOffset(offsetSecs) }
