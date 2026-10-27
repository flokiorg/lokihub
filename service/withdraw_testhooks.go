//go:build testhooks

package service

import (
	"os"

	"github.com/flokiorg/lokihub/cashwithdraw"
)

// withdrawEngineHooks gives a testhooks build's payout engine its holds when
// LOKIHUB_TEST_HOOKS=1, for the regtest suite's withdraw hooks.
func withdrawEngineHooks() *cashwithdraw.EngineHooks {
	if os.Getenv("LOKIHUB_TEST_HOOKS") != "1" {
		return nil
	}
	return &cashwithdraw.EngineHooks{}
}
