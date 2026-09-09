// Command seedrelay publishes integration/testdata/social_graph.json's fixed
// "provider" identity's kind:3 (NIP-02 contact list) follow list, naming
// every fixed "member" identity, to a relay - so the dev relay always has
// this suite's own known-good "following" identities live on it, without
// any test needing to generate a fresh keypair and publish its own follow
// list over the network at test time.
//
// kind:3 is a replaceable event (NIP-01), so re-running this against a
// relay that already has the previous publish is a no-op in effect (the
// relay just keeps the newest one) - safe to call on every `just dev
// reset`, not just once.
//
// Usage: go run ./integration/cmd/seedrelay <relay-ws-url>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

type identity struct {
	Name    string `json:"name"`
	Privkey string `json:"privkey"`
	Pubkey  string `json:"pubkey"`
}

type socialGraph struct {
	Provider identity   `json:"provider"`
	Members  []identity `json:"members"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: seedrelay <relay-ws-url>")
		os.Exit(2)
	}
	relayURL := os.Args[1]

	graph, err := loadSocialGraph()
	if err != nil {
		fmt.Fprintln(os.Stderr, "seedrelay: load social_graph.json:", err)
		os.Exit(1)
	}

	tags := make(nostr.Tags, 0, len(graph.Members))
	for _, m := range graph.Members {
		tags = append(tags, nostr.Tag{"p", m.Pubkey})
	}
	ev := nostr.Event{
		Kind:      nostr.KindFollowList,
		CreatedAt: nostr.Now(),
		Tags:      tags,
	}
	if err := ev.Sign(graph.Provider.Privkey); err != nil {
		fmt.Fprintln(os.Stderr, "seedrelay: sign follow list:", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seedrelay: connect to %s: %v\n", relayURL, err)
		os.Exit(1)
	}
	defer func() { _ = relay.Close() }()

	if err := relay.Publish(ctx, ev); err != nil {
		fmt.Fprintf(os.Stderr, "seedrelay: publish provider kind:3 follow list to %s: %v\n", relayURL, err)
		os.Exit(1)
	}

	fmt.Printf("seedrelay: provider %s now follows %d members on %s\n", graph.Provider.Pubkey, len(graph.Members), relayURL)
}

// loadSocialGraph resolves social_graph.json relative to this source file
// (via runtime.Caller), not the process's working directory, so `go run
// ./integration/cmd/seedrelay` works the same regardless of where it's
// invoked from.
func loadSocialGraph() (*socialGraph, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("could not resolve seedrelay's own source path")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "social_graph.json")

	data, err := os.ReadFile(path) //nolint:gosec // G304: path is built from this file's own runtime.Caller location, not external input
	if err != nil {
		return nil, err
	}
	var graph socialGraph
	if err := json.Unmarshal(data, &graph); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &graph, nil
}
