package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestApplyTieredConfig_OpeningFee_OverwritesFlatFields(t *testing.T) {
	cfg := minimalConfig()
	cfg.OpeningFee = &OpeningFeeConfig{RatePercent: 0.4, MinLoki: 1}

	require.NoError(t, cfg.Validate())
	assert.EqualValues(t, 40, cfg.ChannelFeePermyriad, "0.4%% -> 40 permyriad")
	assert.EqualValues(t, 1000, cfg.ChannelMinimumFeeMloki, "1 loki -> 1000 mloki")
}

func TestApplyTieredConfig_RoutingFee_OverwritesFlatFields(t *testing.T) {
	cfg := minimalConfig()
	cfg.RoutingFee = &RoutingFeeConfig{PPM: 1, BaseLoki: 1}

	require.NoError(t, cfg.Validate())
	assert.Equal(t, 0.000001, cfg.FeeRate, "1 ppm -> 0.000001 bare fraction")
	assert.EqualValues(t, 1000, cfg.BaseFeeMloki, "1 loki -> 1000 mloki")
}

func TestApplyTieredConfig_ChannelSize_OverwritesFlatFields(t *testing.T) {
	cfg := minimalConfig()
	cfg.ChannelSize = &ChannelSizeConfig{MinLoki: 25_000, MaxLoki: 600_000_000, PaddingLoki: 22_000}

	require.NoError(t, cfg.Validate())
	assert.EqualValues(t, 25_000, cfg.MinChannelCapacityLoki)
	assert.EqualValues(t, 22_000, cfg.AdditionalChannelCapacity)
	require.NotNil(t, cfg.Lsps1MinChannelBalanceLoki)
	assert.EqualValues(t, 25_000, *cfg.Lsps1MinChannelBalanceLoki)
	require.NotNil(t, cfg.Lsps1MaxChannelBalanceLoki)
	assert.EqualValues(t, 600_000_000, *cfg.Lsps1MaxChannelBalanceLoki)
	require.NotNil(t, cfg.Lsps1MaxInitialClientBalanceLoki)
	assert.EqualValues(t, 600_000_000, *cfg.Lsps1MaxInitialClientBalanceLoki)
	require.NotNil(t, cfg.Lsps1MaxInitialLspBalanceLoki)
	assert.EqualValues(t, 600_000_000, *cfg.Lsps1MaxInitialLspBalanceLoki)
	require.NotNil(t, cfg.JitMinChannelBalanceLoki)
	assert.EqualValues(t, 25_000, *cfg.JitMinChannelBalanceLoki)
}

func TestApplyTieredConfig_ChannelSize_ZeroMaxLokiLeavesExistingMaxDefaultsAlone(t *testing.T) {
	// A channel_size block that only sets a floor (min_loki/padding_loki,
	// no max_loki) must not clobber the max fields with a bogus 0 — they
	// should fall through to ValidateLsps1's own defaulting instead.
	cfg := minimalConfig()
	cfg.ChannelSize = &ChannelSizeConfig{MinLoki: 25_000, PaddingLoki: 22_000}

	require.NoError(t, cfg.Validate())
	require.NotNil(t, cfg.Lsps1MaxChannelBalanceLoki)
	assert.EqualValues(t, 100_000_000, *cfg.Lsps1MaxChannelBalanceLoki, "falls through to ValidateLsps1's hardcoded default")
}

func TestApplyTieredConfig_UnknownProfileErrors(t *testing.T) {
	cfg := minimalConfig()
	cfg.Profile = "yolo"

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "yolo")
}

func TestApplyTieredConfig_Profile_FillsOnlyUnsetFields(t *testing.T) {
	cfg := minimalConfig()
	cfg.Profile = "aggressive"
	cfg.TimeLockDelta = 999 // explicitly set — must survive the profile

	require.NoError(t, cfg.Validate())
	preset := profilePresets["aggressive"]

	assert.EqualValues(t, 999, cfg.TimeLockDelta, "an explicit value in the YAML must win over the profile default")
	assert.EqualValues(t, preset.targetConf, cfg.TargetConf)
	require.NotNil(t, cfg.MinConfs)
	assert.EqualValues(t, preset.minConfs, *cfg.MinConfs)
	assert.EqualValues(t, preset.maxInactiveDurationSecs, cfg.MaxInactiveDuration)
	require.NotNil(t, cfg.Lsps1OrderExpirySeconds)
	assert.EqualValues(t, preset.lsps1OrderExpirySecs, *cfg.Lsps1OrderExpirySeconds)
	assert.EqualValues(t, preset.jitValidUntilSecs, cfg.JitValidUntilSeconds)
}

func TestApplyTieredConfig_AllThreeProfiles_ProduceValidConfig(t *testing.T) {
	for name := range profilePresets {
		t.Run(name, func(t *testing.T) {
			cfg := minimalConfig()
			cfg.Profile = name
			assert.NoError(t, cfg.Validate())
		})
	}
}

// TestApplyTieredConfig_YAMLIntegration exercises the whole thing through
// real YAML parsing, the way an operator's nodes.yaml would.
func TestApplyTieredConfig_YAMLIntegration(t *testing.T) {
	doc := `
flspdPrivateKey: {from_env: FLSPD_PK}
host: "127.0.0.1:5521"
profile: balanced
opening_fee:
  rate_percent: 0.4
  min_loki: 1
routing_fee:
  ppm: 1
  base_loki: 1
channel_size:
  min_loki: 20000
  max_loki: 500000000
  padding_loki: 21000
`
	var cfg NodeConfig
	require.NoError(t, yaml.Unmarshal([]byte(doc), &cfg))
	require.NoError(t, cfg.Validate())

	assert.EqualValues(t, 40, cfg.ChannelFeePermyriad)
	assert.EqualValues(t, 1000, cfg.ChannelMinimumFeeMloki)
	assert.Equal(t, 0.000001, cfg.FeeRate)
	assert.EqualValues(t, 1000, cfg.BaseFeeMloki)
	assert.EqualValues(t, 20_000, cfg.MinChannelCapacityLoki)
	assert.EqualValues(t, 21_000, cfg.AdditionalChannelCapacity)

	preset := profilePresets["balanced"]
	assert.EqualValues(t, preset.targetConf, cfg.TargetConf)
	assert.EqualValues(t, preset.timeLockDelta, cfg.TimeLockDelta)
}
