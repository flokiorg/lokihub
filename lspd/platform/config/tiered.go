package config

import "fmt"

// This file implements T5-2 through T5-5: a small set of new, optional
// YAML blocks (opening_fee, routing_fee, channel_size, profile) that
// operators can use instead of the older flat, mixed-unit fields
// (channelFeePermyriad, channelMinimumFeeMloki, feeRate, baseFeeMloki, the
// six LSPS1 min/max balance fields, and the individual
// confirmation/timelock/expiry knobs).
//
// Compatibility model: block-level, not field-level. A new block, when
// present in the YAML document, always overwrites the flat field(s) it
// corresponds to — regardless of whether those flat fields also appear in
// the same document. A document that never uses the new block names
// behaves exactly as before; there is no per-field merging between the two
// styles, since distinguishing "explicitly set to the zero value" from
// "left unset" for a plain (non-pointer) flat field isn't reliable, and
// mixing the two styles for the same concept would be confusing to read
// anyway. Pick one style per concept.
//
// applyTieredConfig runs these expansions, then `profile`'s defaulting,
// before Validate()'s existing checks and before ValidateLsps1/ValidateJit
// apply their own hardcoded defaults — so precedence composes correctly:
// an explicit flat-field value in the YAML always wins (it's already been
// unmarshaled by the time this runs), then a new-block value, then a
// profile default, then the existing hardcoded fallback defaults.

// OpeningFeeConfig is the unified LSPS1/LSPS2 channel-opening fee (T5-2,
// T5-3): a percentage of the payment plus a floor, both in operator-facing
// units (percent, loki) rather than permyriad and milliloki.
type OpeningFeeConfig struct {
	// RatePercent is the proportional fee, e.g. 0.4 for 0.4%.
	RatePercent float64 `json:"rate_percent" yaml:"rate_percent"`
	// MinLoki is the fee floor in loki.
	MinLoki uint64 `json:"min_loki" yaml:"min_loki"`
}

// RoutingFeeConfig is the routing fee flspd reports in the
// ChannelInformation RPC (T5-2). Not enforced by flspd itself — the
// channel's actual routing fee is configured on the underlying flnd node —
// this only controls what value is advertised to clients.
type RoutingFeeConfig struct {
	// PPM is the proportional fee rate in parts-per-million.
	PPM uint32 `json:"ppm" yaml:"ppm"`
	// BaseLoki is the flat per-forward fee in loki.
	BaseLoki uint64 `json:"base_loki" yaml:"base_loki"`
}

// ChannelSizeConfig replaces six independent LSPS1/JIT min/max balance
// fields with one block (T5-4): MinLoki/MaxLoki bound both the LSPS1
// channel-balance range and (as a floor) the JIT business-minimum, and
// PaddingLoki is the same headroom AdditionalChannelCapacity added above
// the incoming payment for a JIT open. LSPS1's initial-client-balance and
// initial-lsp-balance ranges (a different concept — how the channel's
// capacity is split at open, not the capacity itself) derive a max of
// MaxLoki and keep their existing 0 floor; set them individually if a
// deployment needs something other than "anywhere up to the channel size
// cap" for those.
type ChannelSizeConfig struct {
	MinLoki     uint64 `json:"min_loki" yaml:"min_loki"`
	MaxLoki     uint64 `json:"max_loki" yaml:"max_loki"`
	PaddingLoki uint64 `json:"padding_loki" yaml:"padding_loki"`
}

// profilePreset is one named bundle of confirmation/timelock/expiry
// defaults (T5-5). These are risk-tolerance defaults, not protocol
// minimums or maximums — every field a preset sets remains individually
// overridable by setting it explicitly in the YAML document.
type profilePreset struct {
	targetConf              uint32
	minConfs                uint32
	timeLockDelta           uint32
	maxInactiveDurationSecs uint64
	lsps1OrderExpirySecs    uint32
	jitValidUntilSecs       uint32
}

// profilePresets are deliberately not exported: the YAML `profile:` field
// is the only supported way to select one, so the preset table can be
// tuned over time without it being part of this package's Go API.
var profilePresets = map[string]profilePreset{
	// conservative: maximize safety margin — more confirmations, a longer
	// CLTV delta, longer-lived channels expected, short-lived offers so
	// stale fee quotes can't be exploited if market conditions move.
	"conservative": {
		targetConf: 6, minConfs: 6, timeLockDelta: 5760, // ~4 days at 1 min/block
		maxInactiveDurationSecs: 7_776_000, // 90 days
		lsps1OrderExpirySecs:    3600,      // 1 hour
		jitValidUntilSecs:       3600,      // 1 hour
	},
	// balanced: matches this repo's own nodes.dev.sample.yaml/nodes.prod.sample.yaml baseline.
	"balanced": {
		targetConf: 6, minConfs: 3, timeLockDelta: 4000, // ~2.8 days at 1 min/block
		maxInactiveDurationSecs: 3_888_000, // 45 days
		lsps1OrderExpirySecs:    86_400,    // 24 hours
		jitValidUntilSecs:       86_400,    // 24 hours
	},
	// aggressive: prioritize speed/UX over safety margin — allows 0-conf
	// (minConfs: 0), a short CLTV delta, and longer-lived offers/channels
	// (more tolerant of idle/slow-to-settle clients).
	"aggressive": {
		targetConf: 1, minConfs: 0, timeLockDelta: 1440, // 1 day at 1 min/block
		maxInactiveDurationSecs: 1_296_000, // 15 days
		lsps1OrderExpirySecs:    172_800,   // 48 hours
		jitValidUntilSecs:       172_800,   // 48 hours
	},
}

// applyTieredConfig expands any of the new blocks present into the
// existing flat fields Validate/ValidateLsps1/ValidateJit and every
// downstream consumer already read. Called at the start of Validate().
func (cfg *NodeConfig) applyTieredConfig() error {
	if cfg.Profile != "" {
		preset, ok := profilePresets[cfg.Profile]
		if !ok {
			return fmt.Errorf("unknown profile %q (must be one of: conservative, balanced, aggressive)", cfg.Profile)
		}
		cfg.applyProfile(preset)
	}

	if cfg.OpeningFee != nil {
		// permyriad is per-10,000; percent * 100 = permyriad
		// (0.4% -> 40 permyriad). min_loki -> milliloki (x1000).
		cfg.ChannelFeePermyriad = int64(cfg.OpeningFee.RatePercent * 100)
		cfg.ChannelMinimumFeeMloki = int64(cfg.OpeningFee.MinLoki * 1000)
	}

	if cfg.RoutingFee != nil {
		// ppm is parts-per-million; ppm / 1e6 = the bare fraction feeRate
		// already uses. base_loki -> milliloki (x1000).
		cfg.FeeRate = float64(cfg.RoutingFee.PPM) / 1_000_000
		cfg.BaseFeeMloki = uint64(cfg.RoutingFee.BaseLoki * 1000)
	}

	if cfg.ChannelSize != nil {
		cs := cfg.ChannelSize
		cfg.MinChannelCapacityLoki = cs.MinLoki
		cfg.AdditionalChannelCapacity = int64(cs.PaddingLoki)
		cfg.Lsps1MinChannelBalanceLoki = &cs.MinLoki
		if cs.MaxLoki > 0 {
			cfg.Lsps1MaxChannelBalanceLoki = &cs.MaxLoki
			cfg.Lsps1MaxInitialClientBalanceLoki = &cs.MaxLoki
			cfg.Lsps1MaxInitialLspBalanceLoki = &cs.MaxLoki
		}
		if cs.MinLoki > 0 {
			cfg.JitMinChannelBalanceLoki = &cs.MinLoki
		}
	}

	return nil
}

// applyProfile fills in only the fields still at their zero value —
// meaning not explicitly set elsewhere in the same YAML document — with
// the preset's values. A plain (non-pointer) zero is an unambiguous "not
// set" signal for every field a profile touches: none of targetConf=0,
// timeLockDelta=0, or maxInactiveDuration=0 is ever a value an operator
// would deliberately choose (0 confirmations-to-confirm, 0 CLTV safety
// margin, or channels closing instantly when idle are not real settings),
// and minConfs/lsps1OrderExpirySeconds are already pointer fields where
// nil unambiguously means unset.
func (cfg *NodeConfig) applyProfile(p profilePreset) {
	if cfg.TargetConf == 0 {
		cfg.TargetConf = p.targetConf
	}
	if cfg.MinConfs == nil {
		minConfs := p.minConfs
		cfg.MinConfs = &minConfs
	}
	if cfg.TimeLockDelta == 0 {
		cfg.TimeLockDelta = p.timeLockDelta
	}
	if cfg.MaxInactiveDuration == 0 {
		cfg.MaxInactiveDuration = p.maxInactiveDurationSecs
	}
	if cfg.Lsps1OrderExpirySeconds == nil {
		expiry := p.lsps1OrderExpirySecs
		cfg.Lsps1OrderExpirySeconds = &expiry
	}
	if cfg.JitValidUntilSeconds == 0 {
		cfg.JitValidUntilSeconds = p.jitValidUntilSecs
	}
}
