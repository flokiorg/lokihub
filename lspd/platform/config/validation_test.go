package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func minimalConfig() *NodeConfig {
	return &NodeConfig{
		MinChannelCapacityLoki: 100_000,
		FeeRate:                0.01,
		BaseFeeMloki:           1000,
	}
}

func TestValidate_AppliesDefaultCapacityWhenZero(t *testing.T) {
	cfg := minimalConfig()
	cfg.MinChannelCapacityLoki = 0

	require.NoError(t, cfg.Validate())
	assert.Equal(t, uint64(AbsoluteMinChannelCapacityLoki), cfg.MinChannelCapacityLoki)
}

func TestValidate_RejectsCapacityBelowAbsoluteFloor(t *testing.T) {
	cfg := minimalConfig()
	cfg.MinChannelCapacityLoki = AbsoluteMinChannelCapacityLoki - 1

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minChannelCapacityLoki")
}

func TestValidate_RejectsNegativeAdditionalCapacity(t *testing.T) {
	cfg := minimalConfig()
	cfg.AdditionalChannelCapacity = -1

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "additionalChannelCapacity")
}

func TestValidate_RejectsFeeRateAboveMax(t *testing.T) {
	cfg := minimalConfig()
	cfg.FeeRate = 0.11

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "feeRate")
}

func TestValidate_RejectsBaseFeeAboveMax(t *testing.T) {
	cfg := minimalConfig()
	cfg.BaseFeeMloki = 100001

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "baseFeeMloki")
}

func ptrU64(v uint64) *uint64 { return &v }

func TestValidateLsps1_DefaultsPassOnMinimalConfig(t *testing.T) {
	cfg := minimalConfig()
	require.NoError(t, cfg.ValidateLsps1())
}

func TestValidateLsps1_RejectsClientBalanceMinGreaterThanMax(t *testing.T) {
	cfg := minimalConfig()
	cfg.Lsps1MinInitialClientBalanceLoki = ptrU64(200_000)
	cfg.Lsps1MaxInitialClientBalanceLoki = ptrU64(100_000)

	err := cfg.ValidateLsps1()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lsps1MinInitialClientBalanceLoki")
}

func TestValidateLsps1_RejectsLspBalanceMinGreaterThanMax(t *testing.T) {
	cfg := minimalConfig()
	cfg.Lsps1MinInitialLspBalanceLoki = ptrU64(200_000)
	cfg.Lsps1MaxInitialLspBalanceLoki = ptrU64(100_000)

	err := cfg.ValidateLsps1()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lsps1MinInitialLspBalanceLoki")
}

func TestValidateLsps1_RejectsChannelBalanceMinGreaterThanMax(t *testing.T) {
	cfg := minimalConfig()
	cfg.Lsps1MinChannelBalanceLoki = ptrU64(200_000)
	cfg.Lsps1MaxChannelBalanceLoki = ptrU64(100_000)

	err := cfg.ValidateLsps1()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lsps1MinChannelBalanceLoki")
}

func TestValidateLsps1_RejectsChannelBalanceBelowNetworkFloor(t *testing.T) {
	cfg := minimalConfig()
	cfg.MinChannelCapacityLoki = 100_000
	cfg.Lsps1MinChannelBalanceLoki = ptrU64(50_000)
	cfg.Lsps1MaxChannelBalanceLoki = ptrU64(100_000_000)

	err := cfg.ValidateLsps1()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network floor")
}

func TestValidateLsps1_DefaultsMinChannelBalanceToNetworkFloor(t *testing.T) {
	cfg := minimalConfig()
	cfg.MinChannelCapacityLoki = 250_000

	require.NoError(t, cfg.ValidateLsps1())
	require.NotNil(t, cfg.Lsps1MinChannelBalanceLoki)
	assert.Equal(t, uint64(250_000), *cfg.Lsps1MinChannelBalanceLoki)
}

func TestValidateJit_SkippedWhenDisabled(t *testing.T) {
	cfg := minimalConfig()
	cfg.JitEnabled = false
	cfg.MinPaymentSizeMloki = 1 // would fail every floor check if evaluated

	require.NoError(t, cfg.ValidateJit())
}

func TestValidateJit_DefaultsMaxClientToSelfDelayWhenZero(t *testing.T) {
	cfg := minimalConfig()
	cfg.JitEnabled = true
	cfg.MinPaymentSizeMloki = cfg.MinChannelCapacityLoki * 1000 // loki -> mloki

	require.NoError(t, cfg.ValidateJit())
	assert.EqualValues(t, 10080, cfg.JitMaxClientToSelfDelay)
}

func TestValidateJit_RejectsPaymentSizeBelowNetworkFloor(t *testing.T) {
	cfg := minimalConfig()
	cfg.JitEnabled = true
	cfg.MinChannelCapacityLoki = 100_000
	cfg.MinPaymentSizeMloki = 1000 // 1 loki, far below the network floor

	err := cfg.ValidateJit()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "network floor")
}

func TestValidateJit_RejectsPaymentSizeBelowBusinessFloor(t *testing.T) {
	cfg := minimalConfig()
	cfg.JitEnabled = true
	cfg.MinChannelCapacityLoki = 100_000
	cfg.MinPaymentSizeMloki = 100_000 * 1000 // exactly at the network floor
	cfg.JitMinChannelBalanceLoki = ptrU64(200_000)

	err := cfg.ValidateJit()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jitMinChannelBalanceLoki")
}

func TestValidateJit_RejectsBusinessFloorBelowNetworkFloor(t *testing.T) {
	cfg := minimalConfig()
	cfg.JitEnabled = true
	cfg.MinChannelCapacityLoki = 100_000
	cfg.MinPaymentSizeMloki = 100_000 * 1000
	cfg.JitMinChannelBalanceLoki = ptrU64(50_000) // below the network floor itself

	err := cfg.ValidateJit()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jitMinChannelBalanceLoki")
	assert.Contains(t, err.Error(), "network floor")
}

func TestValidateJit_RejectsMinGreaterThanMaxPaymentSize(t *testing.T) {
	cfg := minimalConfig()
	cfg.JitEnabled = true
	cfg.MinChannelCapacityLoki = 100_000
	cfg.MinPaymentSizeMloki = 200_000 * 1000
	cfg.JitMaxPaymentSizeMloki = 100_000 * 1000

	err := cfg.ValidateJit()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jitMaxPaymentSizeMloki")
}
