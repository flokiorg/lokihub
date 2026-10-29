package service

import (
	"fmt"

	"github.com/flokiorg/lokihub/lnclient"
)

// testBuildNetworkRefusal refuses to start a binary built with the regtest test
// hooks beside a node that is not on regtest, or whose network it cannot read.
// The HTTP hooks also refuse per request, but hooks that act at start (a clock
// offset, fault switches, withdraw holds) rely on this refusal alone.
func testBuildNetworkRefusal(withHooks bool, info *lnclient.NodeInfo, infoErr error) error {
	if !withHooks {
		return nil
	}
	if infoErr != nil || info == nil {
		return fmt.Errorf("refusing to start a testhooks build: the node's network is unknown (%v)", infoErr)
	}
	if info.Network != "regtest" {
		return fmt.Errorf("refusing to start a testhooks build beside a %q node: test hooks run only on regtest", info.Network)
	}
	return nil
}
