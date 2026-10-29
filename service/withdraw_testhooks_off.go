//go:build !testhooks

package service

import "github.com/flokiorg/lokihub/cashwithdraw"

// withdrawEngineHooks: a production build's payout engine has no test seams.
func withdrawEngineHooks() *cashwithdraw.EngineHooks { return nil }
