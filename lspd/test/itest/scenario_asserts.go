package itest

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func init() {
	assertRegistry["channel_exists"] = assertChannelExists
	assertRegistry["channel_count"] = assertChannelCount
	assertRegistry["channel_count_delta"] = assertChannelCountDelta
}

// --- channel_exists: {between: [<role>, <role>]} ---
// Asserts a channel currently exists between the two named roles (checked
// from the first role's own ListChannels), regardless of confirmation
// state -- callers wanting to assert *confirmed* specifically should check
// that as part of a step instead, since "exists" here also covers the
// zero-conf case where a real ConfirmedScid may not land for a while.

type channelExistsParams struct {
	Between []string `yaml:"between"`
}

func assertChannelExists(t *testing.T, sc *ScenarioContext, params *yaml.Node) {
	var p channelExistsParams
	if err := decodeParamsStrict(params, &p); err != nil {
		require.NoError(t, err)
		return
	}
	if len(p.Between) != 2 {
		t.Errorf("channel_exists: between must list exactly two roles, got %v", p.Between)
		return
	}

	from, to := p.Between[0], p.Between[1]
	fromClient, err := sc.client(from)
	if !assert.NoError(t, err) {
		return
	}
	toPubkeyHex, err := sc.pubkey(to)
	if !assert.NoError(t, err) {
		return
	}

	channels, err := fromClient.ListChannels()
	if !assert.NoError(t, err) {
		return
	}
	for _, ch := range channels {
		if hex.EncodeToString(ch.PeerId) == toPubkeyHex {
			return
		}
	}
	t.Errorf("channel_exists: no channel found between %s and %s", from, to)
}

// --- channel_count: {as: <role>, peer: <role>, count: N} ---
// Asserts an exact channel count between two roles. Only reliable against
// a devnet with no prior channel history between these two specific
// roles (e.g. freshly provisioned) -- this devnet's state is long-lived
// across test runs (see test/devnet/README.md), so channels from earlier,
// unrelated runs accumulate. For a check that's robust to that history,
// use channel_count_delta instead.

type channelCountParams struct {
	As    string `yaml:"as"`
	Peer  string `yaml:"peer"`
	Count int    `yaml:"count"`
}

func assertChannelCount(t *testing.T, sc *ScenarioContext, params *yaml.Node) {
	var p channelCountParams
	if err := decodeParamsStrict(params, &p); err != nil {
		require.NoError(t, err)
		return
	}
	if p.As == "" || p.Peer == "" {
		t.Errorf("channel_count: as and peer are both required")
		return
	}
	got, err := sc.channelCount(p.As, p.Peer)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, p.Count, got, "channel_count: %s must have exactly %d channel(s) with %s, found %d", p.As, p.Count, p.Peer, got)
}

// --- channel_count_delta: {as: <role>, peer: <role>, since: <save_as ref from count_channels>, delta: N} ---
// Asserts the channel count between two roles increased by exactly delta
// since an earlier count_channels snapshot -- this is the F-1 regression
// check: a recovery sweep racing (or duplicating) a live settlement path
// must open exactly one channel for this scenario's own order, not two,
// regardless of how many channels already existed between these roles
// from earlier, unrelated test runs against this same long-lived devnet.

type channelCountDeltaParams struct {
	As    string `yaml:"as"`
	Peer  string `yaml:"peer"`
	Since string `yaml:"since"`
	Delta int    `yaml:"delta"`
}

func assertChannelCountDelta(t *testing.T, sc *ScenarioContext, params *yaml.Node) {
	var p channelCountDeltaParams
	if err := decodeParamsStrict(params, &p); err != nil {
		require.NoError(t, err)
		return
	}
	if p.As == "" || p.Peer == "" || p.Since == "" {
		t.Errorf("channel_count_delta: as, peer, and since are all required")
		return
	}

	saved, err := sc.get(p.Since)
	if !assert.NoError(t, err) {
		return
	}
	baseline, ok := saved.(int)
	if !ok {
		t.Errorf("channel_count_delta: %q was not saved by count_channels", p.Since)
		return
	}

	got, err := sc.channelCount(p.As, p.Peer)
	if !assert.NoError(t, err) {
		return
	}
	assert.Equal(t, p.Delta, got-baseline, "channel_count_delta: %s's channel count with %s must have changed by exactly %d since the count_channels baseline (%d), got %d (now %d)", p.As, p.Peer, p.Delta, baseline, got-baseline, got)
}
