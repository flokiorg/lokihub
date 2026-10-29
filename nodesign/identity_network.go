package nodesign

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// IdentityNetworksKey is the user_configs key holding, for each node identity
// (x-only) cash serving ever started on, the network it started on.
const IdentityNetworksKey = "cash_identity_networks"

// ErrIdentityOnAnotherNetwork: this node identity already signed bills and
// connection strings on another network (NIP-CASH §Mint Provenance: a Hub MUST
// NOT reuse its node identity across networks, since nothing signed names it).
var ErrIdentityOnAnotherNetwork = errors.New("this node identity already signed bills and connection strings on another network")

// ErrUnknownNetwork: the node runs a network with no NIP-47 name.
var ErrUnknownNetwork = errors.New("the node's network has no NIP-47 name")

// CheckIdentityNetwork applies the one-identity-per-network rule to recorded,
// the JSON of IdentityNetworksKey ("" when unset). It returns the JSON to store
// when identity is recorded for the first time, "" when nothing changes.
func CheckIdentityNetwork(recorded, identity, nodeNetwork string) (string, error) {
	network := Network(nodeNetwork)
	if network == "" {
		return "", fmt.Errorf("%w: %q", ErrUnknownNetwork, nodeNetwork)
	}
	networks := map[string]string{}
	if recorded != "" {
		if err := json.Unmarshal([]byte(recorded), &networks); err != nil {
			return "", fmt.Errorf("%s does not parse: %w", IdentityNetworksKey, err)
		}
	}
	if was, ok := networks[identity]; ok {
		if was != network {
			return "", fmt.Errorf("%w: %s (the node now runs %s)", ErrIdentityOnAnotherNetwork, was, network)
		}
		return "", nil
	}
	networks[identity] = network
	out, err := json.Marshal(networks)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// IdentityNetworkGuard runs check until it passes once, then remembers that;
// a failed check is tried again at the next signature. The zero value has no
// check and passes, for a process that never registered one.
type IdentityNetworkGuard struct {
	mu     sync.Mutex
	check  func(ctx context.Context) error
	passed bool
}

// Set replaces the check and forgets an earlier pass.
func (g *IdentityNetworkGuard) Set(check func(ctx context.Context) error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.check, g.passed = check, false
}

// Allow reports why the node identity must not sign now, or nil.
func (g *IdentityNetworkGuard) Allow(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.check == nil || g.passed {
		return nil
	}
	if err := g.check(ctx); err != nil {
		return err
	}
	g.passed = true
	return nil
}

// Guard gates every node-identity signature of a cash payload in this process.
var Guard IdentityNetworkGuard
