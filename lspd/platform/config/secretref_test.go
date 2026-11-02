package config

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestNodesSampleYaml_ParsesAndIsSecretFree parses each of the repo's
// committed nodes.*.sample.yaml files the same way flspd's own loader does
// (cmd/flspd/run.go) and checks each uses from_env/from_file refs, not
// inline secret material, for the three fields SecretRef exists to
// protect — this is what makes the sample files safe to commit verbatim
// (T5-1/T5-4's stated goal), and guards against a repeat of SEC-1 (a real
// secret once committed to data/nodes.prod.yaml).
func TestNodesSampleYaml_ParsesAndIsSecretFree(t *testing.T) {
	for _, name := range []string{"nodes.dev.sample.yaml", "nodes.prod.sample.yaml", "regtest/nodes.regtest.sample.yaml"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("../../deploy/" + name)
			require.NoError(t, err)

			var configs []*NodeConfig
			require.NoError(t, yaml.Unmarshal(data, &configs))
			require.Len(t, configs, 1)

			cfg := configs[0]
			assert.False(t, cfg.FlspdPrivateKey.isInline(), "sample flspdPrivateKey must be a reference (from_env/from_file), not an inline value, to stay safe to commit")
			require.NotNil(t, cfg.Lnd)
			assert.False(t, cfg.Lnd.Cert.isInline(), "sample flnd.cert must be a reference, not inline")
			assert.False(t, cfg.Lnd.Macaroon.isInline(), "sample flnd.macaroon must be a reference, not inline")

			assert.NoError(t, cfg.Validate(), "the committed sample must be a fully valid config, tiered blocks included, not just syntactically parseable YAML")
		})
	}
}

// The regtest sample (lokihub's test LSP, LP-13) announces: its announce
// block is on, valid, and gives the node's fixed address.
func TestNodesRegtestSample_Announces(t *testing.T) {
	data, err := os.ReadFile("../../deploy/regtest/nodes.regtest.sample.yaml")
	require.NoError(t, err)
	var configs []*NodeConfig
	require.NoError(t, yaml.Unmarshal(data, &configs))
	require.Len(t, configs, 1)
	cfg := configs[0]
	require.NoError(t, cfg.Validate())
	assert.True(t, cfg.Announcing())
	assert.True(t, cfg.JitEnabled, "LSPS2 is announced only with JIT on")
	assert.NotEmpty(t, cfg.Tokens, "LSPS2's fee menu needs a token")
	assert.Equal(t, []string{cfg.Host}, cfg.Announce.Addrs)
}

// The dev sample announces too, to the dev stack's local relay.
func TestNodesDevSample_Announces(t *testing.T) {
	data, err := os.ReadFile("../../deploy/nodes.dev.sample.yaml")
	require.NoError(t, err)
	var configs []*NodeConfig
	require.NoError(t, yaml.Unmarshal(data, &configs))
	require.Len(t, configs, 1)
	cfg := configs[0]
	require.NoError(t, cfg.Validate())
	assert.True(t, cfg.Announcing())
	assert.True(t, cfg.JitEnabled, "LSPS2 is announced only with JIT on")
	assert.NotEmpty(t, cfg.Announce.Addrs)
}

func unmarshalSecretRef(t *testing.T, doc string) SecretRef {
	t.Helper()
	var r SecretRef
	require.NoError(t, yaml.Unmarshal([]byte(doc), &r))
	return r
}

func TestSecretRef_InlineScalar_ResolvesToItself(t *testing.T) {
	r := unmarshalSecretRef(t, `"deadbeef"`)
	assert.True(t, r.IsSet())
	v, err := r.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", v)
}

func TestSecretRef_ZeroValue_ResolvesEmpty(t *testing.T) {
	var r SecretRef
	assert.False(t, r.IsSet())
	v, err := r.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "", v)
}

func TestSecretRef_FromEnv_Resolves(t *testing.T) {
	t.Setenv("FLSPD_TEST_SECRET", "cafebabe")
	r := unmarshalSecretRef(t, `from_env: FLSPD_TEST_SECRET`)
	v, err := r.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "cafebabe", v)
}

func TestSecretRef_FromEnv_MissingVarErrors(t *testing.T) {
	if err := os.Unsetenv("FLSPD_TEST_SECRET_NOT_SET"); err != nil {
		t.Fatal(err)
	}
	r := unmarshalSecretRef(t, `from_env: FLSPD_TEST_SECRET_NOT_SET`)
	_, err := r.Resolve()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FLSPD_TEST_SECRET_NOT_SET")
}

func TestSecretRef_FromFile_HexTextContent_UsedVerbatim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.hex")
	require.NoError(t, os.WriteFile(path, []byte("deadbeef\n"), 0o600))

	r := unmarshalSecretRef(t, "from_file: "+path)
	v, err := r.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", v, "trailing whitespace must be trimmed, and already-hex content used as-is rather than double-hex-encoded")
}

func TestSecretRef_FromFile_RawBinaryContent_GetsHexEncoded(t *testing.T) {
	raw := []byte{0x00, 0x01, 0xff, 0xfe, 'n', 'o', 't', ' ', 'h', 'e', 'x'}
	path := filepath.Join(t.TempDir(), "tls.cert")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	r := unmarshalSecretRef(t, "from_file: "+path)
	v, err := r.Resolve()
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(raw), v, "non-hex file content (e.g. a real tls.cert/*.macaroon file) must be hex-encoded automatically")
}

func TestSecretRef_FromFile_MissingFileErrors(t *testing.T) {
	r := unmarshalSecretRef(t, "from_file: /nonexistent/path/for/sure")
	_, err := r.Resolve()
	require.Error(t, err)
}

func TestSecretRef_FromFile_HexEncodingUsesUntrimmedBytes(t *testing.T) {
	// A binary secret that happens to end in a byte equal to a whitespace
	// character must not have that byte silently dropped before
	// hex-encoding — only the *detection* of "is this already hex text"
	// trims, never the fallback encoding path itself.
	raw := []byte{0xaa, 0xbb, 0x20} // trailing 0x20 = ' '
	path := filepath.Join(t.TempDir(), "macaroon.bin")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	r := unmarshalSecretRef(t, "from_file: "+path)
	v, err := r.Resolve()
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(raw), v)
	assert.NotEqual(t, hex.EncodeToString(raw[:len(raw)-1]), v, "the trailing byte must survive hex-encoding, not get trimmed away")
}

func TestSecretRef_UnmarshalYAML_RejectsBothFromEnvAndFromFile(t *testing.T) {
	var r SecretRef
	err := yaml.Unmarshal([]byte("from_env: A\nfrom_file: /b"), &r)
	require.Error(t, err)
}

func TestSecretRef_UnmarshalYAML_RejectsEmptyMapping(t *testing.T) {
	var r SecretRef
	err := yaml.Unmarshal([]byte("{}"), &r)
	require.Error(t, err)
}

func TestSecretRef_UnmarshalYAML_RejectsUnknownShape(t *testing.T) {
	var r SecretRef
	err := yaml.Unmarshal([]byte("- a\n- b"), &r)
	require.Error(t, err)
}

// TestSecretRef_MarshalYAML_RoundTripsOriginalForm is the core regression
// test for why SecretRef isn't resolved in place: the dashboard's
// config-update handler re-serializes a NodeConfig back to YAML on every
// save (dashboard/service.go's handleUpdateConfig), including fields it
// merely copied from the current in-memory config rather than touched.
// If Resolve() mutated the ref, or if MarshalYAML emitted the resolved
// value, a from_env/from_file reference would get silently replaced by
// the literal secret on the very next save.
func TestSecretRef_MarshalYAML_RoundTripsOriginalForm(t *testing.T) {
	cases := []struct {
		name string
		doc  string
	}{
		{"inline", `"deadbeef"`},
		{"from_env", "from_env: SOME_VAR"},
		{"from_file", "from_file: /some/path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := unmarshalSecretRef(t, tc.doc)

			// Simulate resolving it (as e.g. lnd.NewLndClient does) —
			// must not mutate the ref.
			t.Setenv("SOME_VAR", "resolved-value-should-not-leak")
			_, _ = r.Resolve()

			out, err := yaml.Marshal(r)
			require.NoError(t, err)

			var roundTripped SecretRef
			require.NoError(t, yaml.Unmarshal(out, &roundTripped))
			assert.Equal(t, r, roundTripped, "marshaling then unmarshaling must reproduce the exact same ref, not a resolved literal")

			assert.NotContains(t, string(out), "resolved-value-should-not-leak")

			// The struct-equality check above doesn't catch a marshaled
			// document that includes the OTHER (unset) key with an empty
			// value — UnmarshalYAML treats an empty string as "not set"
			// either way, so re-parsing silently absorbs the noise. But a
			// human reading `flspd config show`'s output would see it, so
			// check the raw text too: a from_env ref must never mention
			// from_file, and vice versa.
			switch tc.name {
			case "from_env":
				assert.NotContains(t, string(out), "from_file")
			case "from_file":
				assert.NotContains(t, string(out), "from_env")
			}
		})
	}
}

// TestSecretRef_CopyByValue_PreservesForm exercises the exact mechanism
// dashboard/service.go's handleUpdateConfig relies on: assigning one
// NodeConfig's SecretRef field to another's is a plain Go value copy, and
// must preserve whichever original form (inline/from_env/from_file) it
// held, not collapse to a resolved string.
func TestSecretRef_CopyByValue_PreservesForm(t *testing.T) {
	original := unmarshalSecretRef(t, "from_env: PRESERVE_ME")
	copied := original // plain assignment, as handleUpdateConfig does

	out, err := yaml.Marshal(copied)
	require.NoError(t, err)
	assert.Contains(t, string(out), "PRESERVE_ME")
	assert.Contains(t, string(out), "from_env")
}

func TestSecretRef_InlineSecret_Constructor(t *testing.T) {
	r := InlineSecret("abc123")
	assert.True(t, r.IsSet())
	v, err := r.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "abc123", v)
}

// TestNodeConfig_SecretFields_YAMLIntegration confirms SecretRef behaves
// correctly embedded in the real NodeConfig/LndConfig structs, not just in
// isolation.
func TestNodeConfig_SecretFields_YAMLIntegration(t *testing.T) {
	doc := `
flspdPrivateKey:
  from_env: FLSPD_PK
tokens: ["t1"]
host: "127.0.0.1:5521"
targetConf: 6
minHtlcMloki: 600
baseFeeMloki: 1000
feeRate: 0.000001
timeLockDelta: 40
channelFeePermyriad: 40
channelMinimumFeeMloki: 1000
additionalChannelCapacity: 21000
maxInactiveDuration: 3888000
notificationTimeout: "1m"
flnd:
  address: "flnd:10005"
  cert:
    from_file: /etc/flspd/tls.cert
  macaroon: "0102030405"
`
	var cfg NodeConfig
	require.NoError(t, yaml.Unmarshal([]byte(doc), &cfg))

	t.Setenv("FLSPD_PK", "deadbeef")
	pk, err := cfg.FlspdPrivateKey.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", pk)

	require.NotNil(t, cfg.Lnd)
	assert.True(t, cfg.Lnd.Cert.IsSet())
	mac, err := cfg.Lnd.Macaroon.Resolve()
	require.NoError(t, err)
	assert.Equal(t, "0102030405", mac)

	out, err := yaml.Marshal(&cfg)
	require.NoError(t, err)
	assert.Contains(t, string(out), "from_env")
	assert.Contains(t, string(out), "FLSPD_PK")
	assert.Contains(t, string(out), "from_file")
	assert.Contains(t, string(out), "/etc/flspd/tls.cert")
	assert.NotContains(t, string(out), "deadbeef", "resolved secret must never appear in re-marshaled YAML")
}
