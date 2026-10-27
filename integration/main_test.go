//go:build integration

package integration

import (
	"fmt"
	"os"
	"testing"
)

// TestMain refuses the whole run, whatever -run selects, when the Hub under test
// is not on regtest. The same check in TestAAA_PreflightCleanup only runs when a
// -run filter keeps that test, and a filtered run (the Justfile's own
// integration-ratelimits) against a leftover mainnet config would otherwise
// mint, claim and transfer real funds.
func TestMain(m *testing.M) {
	if cfg, err := LoadConfig(configPathFromEnv()); err == nil {
		if err := regtestGuard(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %v; see integration/README.md\n", err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

// regtestGuard returns an error unless the Hub cfg names answers get_info on
// regtest. With no admin API configured every test skips on its own, so there is
// nothing to check.
func regtestGuard(cfg *Config) error {
	admin, ok := newAdminClient(cfg)
	if !ok {
		return nil
	}
	logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	network, err := hubNetworkLogf(logf, admin)
	if err != nil || network != "regtest" {
		return fmt.Errorf("the Hub under test must be on regtest (get_info network %q, err %v)", network, err)
	}
	return nil
}
