//go:build integration

// Package integration holds a black-box NWC test suite that drives real,
// already-running JIT/circle hub parent connections as an actual NWC client
// would. It is excluded from normal builds/tests by the "integration" build
// tag — run it explicitly with `go test -tags integration ./integration/...`.
//
// Every jit_hub/circle_hub/simple-wallet fixture a test needs is provisioned
// on demand via the admin API (see ephemeral_test.go) and torn down again in
// its own t.Cleanup - config.local.yaml names nothing but that admin API
// itself, so there's no pre-provisioned hub/identity to hand-set-up before
// running this suite.
package integration

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// CircleMembers holds the identities createEphemeralCircleHub authorized
// under one ephemeral circle_hub - see its own doc comment in
// ephemeral_test.go.
type CircleMembers struct {
	AuthorizedPrivkeys []string
}

// JITHubConfig is a ready-to-use jit_hub connection - returned by
// createEphemeralJITHub, never read from config.local.yaml.
type JITHubConfig struct {
	Name       string
	Connection string
}

// CircleHubConfig is a ready-to-use circle_hub connection, plus the member
// identities createEphemeralCircleHub authorized under it - returned by
// createEphemeralCircleHub, never read from config.local.yaml.
type CircleHubConfig struct {
	Name       string
	Connection string
	Members    CircleMembers
}

// SimpleWalletConfig is a ready-to-use plain NWC connection (not a jit_hub or
// circle_hub) granted make_invoice+pay_invoice - returned by
// createEphemeralSimpleWallet, never read from config.local.yaml. Used as an
// external invoice source/payer independent of any specific hub's own
// grants - e.g. draining a JIT child down to exactly zero (rather than a
// partial payment) is what flips its admin/frontend "claimed" state from
// spend-based Active to fully Claimed (see JITHubAllocations.tsx's
// ClaimStateBadge), which needs an invoice for the child's *entire*
// remaining balance.
type SimpleWalletConfig struct {
	Connection string
}

// AdminAPIConfig names the lokihub instance's own admin HTTP API (the same
// one the frontend calls, e.g. POST /api/apps, DELETE /apps/:id/circle/
// children/:childId) - the ONLY thing config.local.yaml needs to name. Every
// jit_hub/circle_hub/simple-wallet fixture is provisioned through it at test
// time (see ephemeral_test.go) rather than hand-set-up beforehand.
//
// Token is a bearer JWT, not a long-lived static API key: mint one by
// calling POST {base_url}/api/unlock with the instance's unlock password,
// permission: "full", and an explicit token_expiry_days (the default is 30
// days) - see http/http_service.go's unlockHandler/createJWT. Because it
// expires, operators need to refresh it here periodically; an expired token
// makes adminClient calls fail loudly rather than skip cleanly, since an
// expired admin token is a config problem, not a hub capability gap.
type AdminAPIConfig struct {
	BaseURL string `yaml:"base_url"`
	Token   string `yaml:"token"`
}

// Config is the top-level shape of config.local.yaml.
type Config struct {
	AdminAPI AdminAPIConfig `yaml:"admin_api"`
}

// defaultConfigPath is used when the INTEGRATION_CONFIG env var is unset.
const defaultConfigPath = "config.local.yaml"

// LoadConfig reads and parses the integration config file. path defaults to
// config.local.yaml (relative to the integration/ package directory) when
// empty.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = defaultConfigPath
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read integration config %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse integration config %q: %w", path, err)
	}

	return &cfg, nil
}

// configPathFromEnv returns the INTEGRATION_CONFIG env var, or "" (meaning
// "use the default") when unset.
func configPathFromEnv() string {
	return os.Getenv("INTEGRATION_CONFIG")
}
