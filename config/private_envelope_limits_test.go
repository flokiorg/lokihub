package config

import (
	"strconv"
	"testing"

	"github.com/flokiorg/lokihub/db/migrations"
	"github.com/flokiorg/lokihub/logger"
	"github.com/ohstr/nmilat/nipcash/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newEnvelopeTestConfig builds a config backed by a throwaway in-memory DB.
// Each test gets its own DSN so stored settings cannot leak between them.
func newEnvelopeTestConfig(t *testing.T, env *AppConfig) *config {
	t.Helper()
	logger.Init(strconv.Itoa(4))

	gdb, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.Migrate(gdb))

	if env == nil {
		env = &AppConfig{}
	}
	env.Workdir = ".test"
	cfg, err := NewConfig(env, gdb)
	require.NoError(t, err)
	return cfg
}

// TestPrivateEnvelopeLimits_FallsBackToSDKDefaults is the bottom layer: a hub
// that configures nothing gets the SDK's defaults, so the hub and every client
// agree on what a valid envelope is without anyone configuring anything.
func TestPrivateEnvelopeLimits_FallsBackToSDKDefaults(t *testing.T) {
	cfg := newEnvelopeTestConfig(t, nil)
	assert.Equal(t, transport.DefaultLimits(), cfg.PrivateEnvelopeLimits())
}

// TestPrivateEnvelopeLimits_EnvOverridesDefaults is the middle layer.
func TestPrivateEnvelopeLimits_EnvOverridesDefaults(t *testing.T) {
	cfg := newEnvelopeTestConfig(t, &AppConfig{
		PrivateEnvelopeMaxBytes:        16 * 1024,
		PrivateEnvelopeMaxItems:        8,
		PrivateEnvelopePadBucketBytes:  2 * 1024,
		PrivateEnvelopeMaxVerifyBudget: 40,
	})

	got := cfg.PrivateEnvelopeLimits()
	assert.Equal(t, 16*1024, got.MaxEnvelopeBytes)
	assert.Equal(t, 8, got.MaxItems)
	assert.Equal(t, 2*1024, got.PadBucketBytes)
	assert.Equal(t, 40, got.MaxVerifyBudget)
}

// TestPrivateEnvelopeLimits_EnvIsPerKnob checks the layers mix: an env var set
// for one knob must not drag the others off their defaults.
func TestPrivateEnvelopeLimits_EnvIsPerKnob(t *testing.T) {
	cfg := newEnvelopeTestConfig(t, &AppConfig{PrivateEnvelopeMaxItems: 4})

	defaults := transport.DefaultLimits()
	got := cfg.PrivateEnvelopeLimits()
	assert.Equal(t, 4, got.MaxItems)
	assert.Equal(t, defaults.MaxEnvelopeBytes, got.MaxEnvelopeBytes)
	assert.Equal(t, defaults.PadBucketBytes, got.PadBucketBytes)
	assert.Equal(t, defaults.MaxVerifyBudget, got.MaxVerifyBudget)
}

// TestPrivateEnvelopeLimits_HubSettingBeatsEnv is the top layer — the whole point
// of the user-facing setting.
func TestPrivateEnvelopeLimits_HubSettingBeatsEnv(t *testing.T) {
	cfg := newEnvelopeTestConfig(t, &AppConfig{
		PrivateEnvelopeMaxBytes: 16 * 1024,
		PrivateEnvelopeMaxItems: 8,
	})

	stored := transport.Limits{
		MaxEnvelopeBytes: 32 * 1024,
		MaxItems:         12,
		PadBucketBytes:   8 * 1024,
		MaxVerifyBudget:  60,
	}
	require.NoError(t, cfg.SetPrivateEnvelopeLimits(stored))
	assert.Equal(t, stored, cfg.PrivateEnvelopeLimits())
}

// TestSetPrivateEnvelopeLimits_RefusesAboveTheNIP44Ceiling is the important one:
// a hub must not be able to store a policy that makes every envelope fail. NIP-44
// refuses plaintext over 65535 bytes, and no retry would fix that.
func TestSetPrivateEnvelopeLimits_RefusesAboveTheNIP44Ceiling(t *testing.T) {
	cfg := newEnvelopeTestConfig(t, nil)

	bad := transport.DefaultLimits()
	bad.MaxEnvelopeBytes = transport.MaxNIP44Plaintext + 1
	require.Error(t, cfg.SetPrivateEnvelopeLimits(bad))

	// And nothing was persisted, so reads still return a usable policy.
	assert.Equal(t, transport.DefaultLimits(), cfg.PrivateEnvelopeLimits())
}

func TestSetPrivateEnvelopeLimits_RefusesIncoherentPolicy(t *testing.T) {
	cfg := newEnvelopeTestConfig(t, nil)

	for name, bad := range map[string]transport.Limits{
		"zero bytes":               {MaxEnvelopeBytes: 0, MaxItems: 8, PadBucketBytes: 1024, MaxVerifyBudget: 10},
		"zero items":               {MaxEnvelopeBytes: 8192, MaxItems: 0, PadBucketBytes: 1024, MaxVerifyBudget: 10},
		"negative verify budget":   {MaxEnvelopeBytes: 8192, MaxItems: 8, PadBucketBytes: 1024, MaxVerifyBudget: -1},
		"pad bucket above ceiling": {MaxEnvelopeBytes: 8192, MaxItems: 8, PadBucketBytes: 9000, MaxVerifyBudget: 10},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, cfg.SetPrivateEnvelopeLimits(bad))
		})
	}
}

// TestPrivateEnvelopeLimits_IgnoresUnparseableStoredValue pins that a corrupt
// setting degrades to a working policy rather than bricking the transport. A hub
// that cannot parse its own limits must still serve.
func TestPrivateEnvelopeLimits_IgnoresUnparseableStoredValue(t *testing.T) {
	cfg := newEnvelopeTestConfig(t, nil)

	require.NoError(t, cfg.SetUpdate(privateEnvelopeMaxItemsKey, "not-a-number", ""))
	assert.Equal(t, transport.DefaultLimits().MaxItems, cfg.PrivateEnvelopeLimits().MaxItems)

	require.NoError(t, cfg.SetUpdate(privateEnvelopeMaxItemsKey, "-5", ""))
	assert.Equal(t, transport.DefaultLimits().MaxItems, cfg.PrivateEnvelopeLimits().MaxItems)
}

// TestPrivateEnvelopeLimits_StoredPolicyAlwaysValidates guards the read path: if
// a stored combination is somehow invalid as a whole, the getter must return
// defaults rather than a policy that rejects everything.
func TestPrivateEnvelopeLimits_StoredPolicyAlwaysValidates(t *testing.T) {
	cfg := newEnvelopeTestConfig(t, nil)

	// Write past the setter's validation to simulate a hand-edited or
	// downgrade-corrupted settings row.
	require.NoError(t, cfg.SetUpdate(privateEnvelopeMaxBytesKey, strconv.Itoa(transport.MaxNIP44Plaintext+1), ""))

	got := cfg.PrivateEnvelopeLimits()
	require.NoError(t, got.Validate(), "the getter must never hand back an unusable policy")
	assert.Equal(t, transport.DefaultLimits(), got)
}
