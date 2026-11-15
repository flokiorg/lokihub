//go:build testhooks

package service

import "testing"

// The testhooks tag is what turns the start-time refusal on.
func TestTestHooksBuildIsMarked(t *testing.T) {
	if !buildHasTestHooks {
		t.Fatal("a testhooks build must mark itself, or it would start beside any node")
	}
}
