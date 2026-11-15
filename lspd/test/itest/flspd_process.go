package itest

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var (
	flspdBinaryOnce sync.Once
	flspdBinaryPath string
	flspdBinaryErr  error
)

// buildFlspdBinary compiles cmd/flspd once per test binary run (not once
// per FlspdProcess) and returns the path to the resulting executable.
func buildFlspdBinary(t *testing.T) string {
	t.Helper()

	flspdBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "flspd-itest-bin-*")
		if err != nil {
			flspdBinaryErr = err
			return
		}
		out := filepath.Join(dir, "flspd")

		cmd := exec.Command("go", "build", "-o", out, "./cmd/lspd")
		cmd.Dir = filepath.Join(devnetDir(t), "..", "..") // test/devnet/../.. -> repo root
		combined, err := cmd.CombinedOutput()
		if err != nil {
			flspdBinaryErr = fmt.Errorf("go build ./cmd/lspd: %w\n%s", err, combined)
			return
		}
		flspdBinaryPath = out
	})

	if flspdBinaryErr != nil {
		t.Fatalf("building flspd binary: %v", flspdBinaryErr)
	}
	return flspdBinaryPath
}

// killStrayFlspdProcesses is a defensive sweep run before any devnet-backed
// test starts. Pdeathsig (set in Start below) should make every flspd
// subprocess die with its parent test process, but Go's runtime can migrate
// goroutines across OS threads in ways that make Pdeathsig unreliable in
// practice — and critically, go test's own -timeout watchdog kills the test
// process without running t.Cleanup at all, so a hung test can orphan its
// flspd child regardless. An orphan stays connected to the devnet's
// flnd-lsp and keeps answering LSPS2 custom messages from every later test
// run, corrupting them with an extra, independently-generated response
// (observed firsthand: two get_info replies for one request, each with its
// own promise/timestamp, one from the live process and one from a ~20
// minute-old orphan). Since every flspd binary this package builds lives
// under a uniquely-named /tmp/flspd-itest-bin-* directory, matching on that
// path is specific enough to not risk killing an unrelated process.
func killStrayFlspdProcesses(t *testing.T) {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", "flspd-itest-bin-.*/flspd").Output()
	if err != nil {
		return // pgrep exits non-zero when there are no matches; nothing to do
	}
	for _, pidStr := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			if err := proc.Kill(); err == nil {
				t.Logf("killed stray orphaned flspd process (pid %d) from an earlier test run", pid)
			}
		}
	}
}

// FlspdProcessOptions configures the generated flspd NodeConfig. Zero
// values fall back to fast, test-friendly defaults (see NewFlspdProcess).
type FlspdProcessOptions struct {
	JitEnabled bool

	// MempoolAPIBaseURL points flspd's chain-fee estimation at a fake
	// mempool API (see FakeMempoolServer) instead of the config-only
	// DefaultFeeEstimator it otherwise falls back to -- needed for
	// mempool fee-congestion fault injection (T4-3). Empty means "use
	// the default," matching production's own behavior when
	// MEMPOOL_API_BASE_URL isn't set.
	MempoolAPIBaseURL string

	// Announce is the node's NIP-LSP announce block; nil leaves it off.
	Announce *config.AnnounceConfig

	// Env adds environment variables (e.g. NOSTR_ANNOUNCE_RELAYS,
	// DASHBOARD_ADMIN_PUBKEY).
	Env []string
}

// FlspdProcess runs the actual flspd binary under test as a real OS
// subprocess against a devnet role (normally "lsp") and a disposable
// Postgres instance — not a Go-level fake. Start/Stop/Kill/Restart give
// scenarios (notably the F-1 crash-recovery one) direct control over its
// lifecycle.
type FlspdProcess struct {
	t             *testing.T
	binaryPath    string
	configPath    string
	listenAddr    string
	dashboardAddr string
	env           []string

	mu  sync.Mutex
	cmd *exec.Cmd
}

// NewFlspdProcess generates a one-node NodeConfig YAML pointed at the
// devnet's "lsp" flnd role and the given Postgres instance, ready to
// Start(). The private key is freshly generated per process (each call
// gets its own, matching how genkey.go documents doing this for real
// deployments).
func NewFlspdProcess(t *testing.T, d *Devnet, pg *PostgresContainer, opts FlspdProcessOptions) *FlspdProcess {
	t.Helper()

	lndConf, err := d.LndConfigFor("lsp")
	require.NoError(t, err, "reading lsp lnd config")

	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err, "generating flspd private key")

	listenPort, err := getPort()
	require.NoError(t, err, "allocating flspd listen port")
	listenAddr := fmt.Sprintf("127.0.0.1:%d", listenPort)

	dashboardPort, err := getPort()
	require.NoError(t, err, "allocating flspd dashboard port")
	dashboardAddr := fmt.Sprintf("127.0.0.1:%d", dashboardPort)

	minConfs := uint32(1)
	nodeConf := &config.NodeConfig{
		Name:                      "itest-lsp",
		FlspdPrivateKey:           config.InlineSecret(fmt.Sprintf("%x", priv.Serialize())),
		Tokens:                    []string{"itest-token"},
		Host:                      lndConf.Address,
		TargetConf:                1,
		MinConfs:                  &minConfs,
		MinHtlcMloki:              600,
		BaseFeeMloki:              1000,
		FeeRate:                   0.000001,
		TimeLockDelta:             800, // matches flnd's chain-wide default (chainreg.DefaultFlokicoinTimeLockDelta)
		ChannelFeePermyriad:       40,
		ChannelMinimumFeeMloki:    1000,
		AdditionalChannelCapacity: 21000,
		MinChannelCapacityLoki:    20000,
		MaxInactiveDuration:       3888000,
		NotificationTimeout:       "10s",
		MinPaymentSizeMloki:       1000,
		JitEnabled:                opts.JitEnabled,
		JitMaxPaymentSizeMloki:    300_000_000_000,
		JitMaxClientToSelfDelay:   10000,
		JitValidUntilSeconds:      86400,
		Announce:                  opts.Announce,
		Lnd: &config.LndConfig{
			Address:    lndConf.Address,
			Cert:       lndConf.Cert,
			Macaroon:   lndConf.Macaroon,
			DisableTls: false,
		},
	}

	yamlBytes, err := yaml.Marshal([]*config.NodeConfig{nodeConf})
	require.NoError(t, err, "marshaling generated NodeConfig")

	dir, err := os.MkdirTemp("", "flspd-itest-config-*")
	require.NoError(t, err, "creating flspd config tempdir")
	configPath := filepath.Join(dir, "nodes.yaml")
	require.NoError(t, os.WriteFile(configPath, yamlBytes, 0o600), "writing generated NodeConfig")

	env := append(os.Environ(),
		"NODES_CONFIG_PATH="+configPath,
		"DATABASE_URL="+pg.ConnectionString(),
		"AUTO_MIGRATE_DATABASE=true",
		"LISTEN_ADDRESS="+listenAddr,
		"DASHBOARD_LISTEN_ADDRESS="+dashboardAddr,
		"LSP_NOSTR_PRIVATE_KEY=",
	)
	if opts.MempoolAPIBaseURL != "" {
		env = append(env, "MEMPOOL_API_BASE_URL="+opts.MempoolAPIBaseURL)
	}
	env = append(env, opts.Env...)

	return &FlspdProcess{
		t:             t,
		binaryPath:    buildFlspdBinary(t),
		configPath:    configPath,
		listenAddr:    listenAddr,
		dashboardAddr: dashboardAddr,
		env:           env,
	}
}

// Start launches the flspd subprocess and waits for its gRPC listener to
// accept connections.
func (p *FlspdProcess) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.cmd != nil {
		return fmt.Errorf("flspd process already started")
	}

	cmd := exec.Command(p.binaryPath)
	cmd.Env = p.env
	// If this test process is killed hard (notably go test's own -timeout
	// watchdog, which os.Exits without running t.Cleanup), the flspd child
	// would otherwise be orphaned and keep running indefinitely — it stays
	// connected to the devnet's flnd-lsp and keeps answering LSPS2 requests
	// from every *later* test run too, corrupting them with duplicate
	// responses carrying stale promises. Pdeathsig makes the kernel SIGKILL
	// it the moment this process dies, by any means.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	logPath := p.configPath + ".log"
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("creating flspd log file: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("starting flspd process: %w", err)
	}
	p.cmd = cmd
	p.t.Logf("flspd started (pid %d), logs at %s", cmd.Process.Pid, logPath)

	return p.waitForListener(30 * time.Second)
}

func (p *FlspdProcess) waitForListener(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", p.listenAddr, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("flspd never started listening on %s within %s (see %s.log)", p.listenAddr, timeout, p.configPath)
}

// Stop sends SIGTERM and waits for a graceful shutdown.
func (p *FlspdProcess) Stop() error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	return p.wait(10 * time.Second)
}

// Kill sends SIGKILL — no graceful shutdown, simulating a crash. This is
// what the F-1 recovery scenario uses to reproduce "flspd died mid-flight".
func (p *FlspdProcess) Kill() error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil {
		return err
	}
	return p.wait(10 * time.Second)
}

func (p *FlspdProcess) wait(timeout time.Duration) error {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		p.mu.Lock()
		p.cmd = nil
		p.mu.Unlock()
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("flspd process did not exit within %s", timeout)
	}
}

// Restart kills the process (simulating a crash, not a graceful stop —
// the interesting recovery case) and starts a fresh instance with the
// exact same config, so it reconnects to the same flnd role and Postgres
// database and runs its startup recovery sweep.
func (p *FlspdProcess) Restart() error {
	if err := p.Kill(); err != nil {
		return err
	}
	return p.Start()
}

// ListenAddress returns the address flspd's gRPC API is bound to.
func (p *FlspdProcess) ListenAddress() string {
	return p.listenAddr
}

// DashboardAddress returns the address flspd's dashboard is bound to.
func (p *FlspdProcess) DashboardAddress() string {
	return p.dashboardAddr
}

// Context is a convenience for steps that need a bounded context tied to
// the process's own test.
func (p *FlspdProcess) Context() context.Context {
	return p.t.Context()
}
