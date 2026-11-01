package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli"
)

const validSampleConfig = `
- name: "test-node"
  flspdPrivateKey: {from_env: TEST_FLSPD_PK}
  tokens: ["tok"]
  host: "127.0.0.1:5521"
  targetConf: 6
  minConfs: 3
  minHtlcMloki: 600
  baseFeeMloki: 1000
  feeRate: 0.000001
  timeLockDelta: 400
  channelFeePermyriad: 40
  channelMinimumFeeMloki: 1000
  additionalChannelCapacity: 21000
  minChannelCapacityLoki: 20000
  maxInactiveDuration: 3888000
  notificationTimeout: "1m"
  minPaymentSizeMloki: 1000
  jitMaxPaymentSizeMloki: 300000000000
  jitEnabled: false
  flnd:
    address: "flnd:10005"
    cert: {from_env: TEST_FLND_CERT}
    macaroon: {from_env: TEST_FLND_MAC}
`

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// --- loadNodeConfigs ---

func TestLoadNodeConfigs_FromFile(t *testing.T) {
	path := writeTempConfig(t, validSampleConfig)
	t.Setenv("NODES_CONFIG_PATH", path)
	t.Setenv("NODES_JSON", "")

	configs, err := loadNodeConfigs()
	require.NoError(t, err)
	require.Len(t, configs, 1)
	assert.Equal(t, "test-node", configs[0].Name)
}

func TestLoadNodeConfigs_FromNodesJsonEnvVar(t *testing.T) {
	t.Setenv("NODES_JSON", `[{"name":"json-node","host":"h","flspdPrivateKey":"deadbeef","tokens":["t"],"targetConf":1,"minHtlcMloki":1,"baseFeeMloki":1,"feeRate":0.01,"timeLockDelta":1,"channelFeePermyriad":1,"channelMinimumFeeMloki":1,"maxInactiveDuration":1,"notificationTimeout":"1m"}]`)

	configs, err := loadNodeConfigs()
	require.NoError(t, err)
	require.Len(t, configs, 1)
	assert.Equal(t, "json-node", configs[0].Name)
}

func TestLoadNodeConfigs_NeitherSourceSetErrors(t *testing.T) {
	t.Setenv("NODES_JSON", "")
	t.Setenv("NODES_CONFIG_PATH", "")

	_, err := loadNodeConfigs()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NODES_CONFIG_PATH")
}

func TestLoadNodeConfigs_EmptyListErrors(t *testing.T) {
	path := writeTempConfig(t, "[]")
	t.Setenv("NODES_CONFIG_PATH", path)
	t.Setenv("NODES_JSON", "")

	_, err := loadNodeConfigs()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one node")
}

func TestLoadNodeConfigs_MalformedYamlErrors(t *testing.T) {
	path := writeTempConfig(t, "not: [valid, yaml: structure")
	t.Setenv("NODES_CONFIG_PATH", path)
	t.Setenv("NODES_JSON", "")

	_, err := loadNodeConfigs()
	require.Error(t, err)
}

// --- nodeLabel ---

func TestNodeLabel(t *testing.T) {
	path := writeTempConfig(t, validSampleConfig)
	t.Setenv("NODES_CONFIG_PATH", path)
	t.Setenv("NODES_JSON", "")
	configs, err := loadNodeConfigs()
	require.NoError(t, err)

	assert.Equal(t, "test-node", nodeLabel(configs[0]))

	configs[0].Name = ""
	configs[0].NodePubkey = "02abc"
	assert.Equal(t, "02abc", nodeLabel(configs[0]))

	configs[0].NodePubkey = ""
	assert.Equal(t, "(unnamed)", nodeLabel(configs[0]))
}

// --- CLI integration: run the actual commands end-to-end ---

func newTestApp() *cli.App {
	app := cli.NewApp()
	app.Commands = []cli.Command{configCommand}
	return app
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

func TestConfigCheckCommand_ValidConfigResolvableSecrets(t *testing.T) {
	path := writeTempConfig(t, validSampleConfig)
	t.Setenv("NODES_CONFIG_PATH", path)
	t.Setenv("NODES_JSON", "")
	t.Setenv("TEST_FLSPD_PK", "deadbeef")
	t.Setenv("TEST_FLND_CERT", "cafe")
	t.Setenv("TEST_FLND_MAC", "babe")

	var runErr error
	out := captureStdout(t, func() {
		runErr = newTestApp().Run([]string{"flspd", "config", "check"})
	})
	require.NoError(t, runErr)
	assert.Contains(t, out, "OK")
}

func TestConfigCheckCommand_UnresolvableSecretFails(t *testing.T) {
	path := writeTempConfig(t, validSampleConfig)
	t.Setenv("NODES_CONFIG_PATH", path)
	t.Setenv("NODES_JSON", "")
	if err := os.Unsetenv("TEST_FLSPD_PK"); err != nil { // deliberately not set
		t.Fatal(err)
	}
	t.Setenv("TEST_FLND_CERT", "cafe")
	t.Setenv("TEST_FLND_MAC", "babe")

	runErr := newTestApp().Run([]string{"flspd", "config", "check"})
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "flspdPrivateKey")
	assert.Contains(t, runErr.Error(), "TEST_FLSPD_PK")
}

func TestConfigCheckCommand_InvalidBusinessRulesFails(t *testing.T) {
	// minChannelCapacityLoki below the absolute floor (20000).
	invalid := `
- name: "bad-node"
  flspdPrivateKey: {from_env: TEST_FLSPD_PK}
  tokens: ["tok"]
  host: "127.0.0.1:5521"
  targetConf: 6
  minHtlcMloki: 600
  baseFeeMloki: 1000
  feeRate: 0.000001
  timeLockDelta: 400
  channelFeePermyriad: 40
  channelMinimumFeeMloki: 1000
  minChannelCapacityLoki: 100
  maxInactiveDuration: 3888000
  notificationTimeout: "1m"
`
	path := writeTempConfig(t, invalid)
	t.Setenv("NODES_CONFIG_PATH", path)
	t.Setenv("NODES_JSON", "")

	runErr := newTestApp().Run([]string{"flspd", "config", "check"})
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "minChannelCapacityLoki")
}

func TestConfigShowCommand_PrintsResolvedTieredBlocksAndRefForm(t *testing.T) {
	// Deliberately doesn't set minConfs itself, so the aggressive
	// profile's minConfs:0 default is actually visible in the output
	// rather than being pre-empted by an explicit value (validSampleConfig
	// sets minConfs: 3, which — correctly — would win over any profile).
	withoutMinConfs := `
- name: "test-node"
  flspdPrivateKey: {from_env: TEST_FLSPD_PK}
  tokens: ["tok"]
  host: "127.0.0.1:5521"
  minHtlcMloki: 600
  baseFeeMloki: 1000
  feeRate: 0.000001
  channelFeePermyriad: 40
  channelMinimumFeeMloki: 1000
  minChannelCapacityLoki: 20000
  notificationTimeout: "1m"
  profile: aggressive
  flnd:
    address: "flnd:10005"
    cert: {from_env: TEST_FLND_CERT}
    macaroon: {from_env: TEST_FLND_MAC}
`
	path := writeTempConfig(t, withoutMinConfs)
	t.Setenv("NODES_CONFIG_PATH", path)
	t.Setenv("NODES_JSON", "")
	t.Setenv("TEST_FLSPD_PK", "deadbeef")
	t.Setenv("TEST_FLND_CERT", "cafe")
	t.Setenv("TEST_FLND_MAC", "babe")

	var runErr error
	out := captureStdout(t, func() {
		runErr = newTestApp().Run([]string{"flspd", "config", "show"})
	})
	require.NoError(t, runErr)

	assert.Contains(t, out, "from_env: TEST_FLSPD_PK", "secret must show its reference form, not the resolved value")
	assert.NotContains(t, out, "deadbeef", "resolved secret must never be printed")
	assert.Contains(t, out, "minConfs: 0", "aggressive profile's minConfs:0 must have been expanded and shown")
}

func TestConfigExplainFeesCommand_MatchesComputeOpeningFee(t *testing.T) {
	path := writeTempConfig(t, validSampleConfig)
	t.Setenv("NODES_CONFIG_PATH", path)
	t.Setenv("NODES_JSON", "")
	t.Setenv("TEST_FLSPD_PK", "deadbeef")
	t.Setenv("TEST_FLND_CERT", "cafe")
	t.Setenv("TEST_FLND_MAC", "babe")

	var runErr error
	out := captureStdout(t, func() {
		runErr = newTestApp().Run([]string{"flspd", "config", "explain", "fees", "--payment-loki", "5000000"})
	})
	require.NoError(t, runErr)
	// channelFeePermyriad=40 -> 0.4%; 5,000,000 loki * 0.4% = 20,000 loki.
	assert.Contains(t, out, "20000 loki")
}

func TestConfigCommands_NodeIndexOutOfRange(t *testing.T) {
	path := writeTempConfig(t, validSampleConfig)
	t.Setenv("NODES_CONFIG_PATH", path)
	t.Setenv("NODES_JSON", "")

	runErr := newTestApp().Run([]string{"flspd", "config", "show", "--node-index", "5"})
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "out of range")
}
