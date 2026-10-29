//go:build !testhooks

package service

// buildHasTestHooks: release builds carry no test hooks.
const buildHasTestHooks = false
