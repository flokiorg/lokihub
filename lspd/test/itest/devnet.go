package itest

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/invoicesrpc"
	"github.com/flokiorg/flnd/lnrpc/routerrpc"
	"github.com/flokiorg/lokihub/lspd/adapters/lnd"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/config"
)

// devnetRole describes one of test/devnet/docker-compose.yml's named flnd
// roles, matching its published host ports (overridable by the same
// DEVNET_*_PORT env vars the compose file itself reads).
type devnetRole struct {
	name        string
	rpcPortEnv  string
	defaultPort uint32
}

var devnetRoles = map[string]devnetRole{
	"lsp":    {"lsp", "DEVNET_LSP_RPC_PORT", 20001},
	"client": {"client", "DEVNET_CLIENT_RPC_PORT", 20002},
	"payer":  {"payer", "DEVNET_PAYER_RPC_PORT", 20003},
}

// Devnet is a handle onto the native-Flokicoin regtest network defined by
// test/devnet/docker-compose.yml (see test/devnet/README.md): a lokid
// chain plus three named flnd roles (lsp/client/payer), connected via
// neutrino — no bitcoin-core anywhere. It is deliberately NOT torn down
// between test runs (lokid wipes its regtest DB on every restart, so
// StartDevnet only starts what isn't already running) — treat the whole
// `go test` invocation as one disposable session, matching the manual
// workflow in test/devnet/README.md.
type Devnet struct {
	dir string
}

// StartDevnet ensures the devnet's docker-compose services are up (a no-op
// for any already-running container) and waits for all three flnd roles
// to respond to GetInfo. Skips the calling test if docker or the devnet
// directory isn't available, rather than failing — this suite requires
// heavyweight local infra and isn't part of the standard `go test ./...`
// run (see .github/workflows/ci.yml's itest exclusion).
func StartDevnet(t *testing.T) *Devnet {
	t.Helper()

	dir := devnetDir(t)
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("devnet directory not found at %s: %v", dir, err)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available, skipping devnet-backed test")
	}

	killStrayFlspdProcesses(t)

	d := &Devnet{dir: dir}

	cmd := exec.Command("docker", "compose", "up", "-d")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("docker compose up failed (is the devnet built? see test/devnet/README.md): %v\n%s", err, out)
	}

	for role := range devnetRoles {
		d.waitForRole(t, role, 60*time.Second)
	}

	// The devnet's chain doesn't advance on its own; flnd's synced_to_chain
	// flag depends on the best header timestamp being recent, so a stale
	// (session-old) chain can report unsynced even though it's otherwise
	// healthy. One fresh block fixes it cheaply and deterministically.
	if err := d.MineBlocks(1); err != nil {
		t.Fatalf("failed to mine a freshness block on devnet startup: %v", err)
	}

	return d
}

func devnetDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd(): %v", err)
	}
	// itest/ lives at test/itest/ (T6-5); devnet/ is its sibling at
	// test/devnet/.
	return filepath.Join(wd, "..", "devnet")
}

func (d *Devnet) roleDataDir(role string) string {
	return filepath.Join(d.dir, "data", "flnd-"+role)
}

func (d *Devnet) rolePort(role string) uint32 {
	r := devnetRoles[role]
	if v := os.Getenv(r.rpcPortEnv); v != "" {
		if port, err := strconv.ParseUint(v, 10, 32); err == nil {
			return uint32(port)
		}
	}
	return r.defaultPort
}

// lndConfigFor builds the config.LndConfig NewLndClient expects (hex-encoded
// PEM cert, hex-encoded macaroon bytes) by reading the devnet role's
// bind-mounted TLS/macaroon files directly off the host filesystem.
func (d *Devnet) lndConfigFor(role string) (*config.LndConfig, error) {
	dataDir := d.roleDataDir(role)

	certBytes, err := os.ReadFile(filepath.Join(dataDir, "tls.cert"))
	if err != nil {
		return nil, fmt.Errorf("reading tls.cert for role %q: %w", role, err)
	}
	macBytes, err := os.ReadFile(filepath.Join(dataDir, "data", "chain", "flokicoin", "regtest", "admin.macaroon"))
	if err != nil {
		return nil, fmt.Errorf("reading admin.macaroon for role %q: %w", role, err)
	}

	return &config.LndConfig{
		Address:  fmt.Sprintf("127.0.0.1:%d", d.rolePort(role)),
		Cert:     config.InlineSecret(hex.EncodeToString(certBytes)),
		Macaroon: config.InlineSecret(hex.EncodeToString(macBytes)),
	}, nil
}

// LndConfigFor exposes lndConfigFor for callers that need the raw config
// (e.g. to embed a role's connection details into a generated flspd
// NodeConfig YAML), not just a ready client.
func (d *Devnet) LndConfigFor(role string) (*config.LndConfig, error) {
	return d.lndConfigFor(role)
}

// LightningClient returns flspd's own production lnd.LndClient wired
// against the given devnet role, so scenario steps/assertions exercise the
// exact same gRPC client code flspd uses in production rather than a
// separate test-only implementation. StartListeners is called here (not
// left to the caller) since it's required for IsConnected/GetPeerId's
// peer-cache (P-2) to ever be populated -- production's own callers
// (cmd/flspd/run.go) call it right after construction too, for the same
// reason.
func (d *Devnet) LightningClient(role string) (lightning.Client, error) {
	conf, err := d.lndConfigFor(role)
	if err != nil {
		return nil, err
	}
	client, err := lnd.NewLndClient(conf)
	if err != nil {
		return nil, err
	}
	client.StartListeners()
	return client, nil
}

// RawLightningClient returns the same connection as LightningClient but as
// the underlying lnrpc.LightningClient, for scenario steps that need gRPC
// methods (AddInvoice, ConnectPeer, custom messages) the lightning.Client
// port doesn't expose.
func (d *Devnet) RawLightningClient(role string) (lnrpc.LightningClient, error) {
	client, err := d.LightningClient(role)
	if err != nil {
		return nil, err
	}
	lndClient, ok := client.(*lnd.LndClient)
	if !ok {
		return nil, fmt.Errorf("role %q client is not *lnd.LndClient", role)
	}
	return lndClient.RawClient(), nil
}

// RawRouterClient is RawLightningClient's counterpart for routerrpc, needed
// to actually send a payment (SendPaymentV2) from a non-LSP role.
func (d *Devnet) RawRouterClient(role string) (routerrpc.RouterClient, error) {
	client, err := d.LightningClient(role)
	if err != nil {
		return nil, err
	}
	lndClient, ok := client.(*lnd.LndClient)
	if !ok {
		return nil, fmt.Errorf("role %q client is not *lnd.LndClient", role)
	}
	return lndClient.RouterClient(), nil
}

// RawInvoicesClient exposes role's raw invoicesrpc.InvoicesClient, for the
// same reason as RawRouterClient -- the H-12 devnet verification test needs
// to drive SubscribeSingleInvoice/AddHoldInvoice directly against real
// flnd, independent of flspd's own wrapper, to prove the underlying RPC
// actually delivers ACCEPTED rather than trusting the proto docs (this
// fork has diverged from upstream LND invoice-subscription behavior
// before -- see heldInvoiceSafetyNetPollInterval's doc comment in
// lsps/settlement_service.go).
func (d *Devnet) RawInvoicesClient(role string) (invoicesrpc.InvoicesClient, error) {
	client, err := d.LightningClient(role)
	if err != nil {
		return nil, err
	}
	lndClient, ok := client.(*lnd.LndClient)
	if !ok {
		return nil, fmt.Errorf("role %q client is not *lnd.LndClient", role)
	}
	return lndClient.InvoicesClient(), nil
}

func (d *Devnet) waitForRole(t *testing.T, role string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		client, err := d.LightningClient(role)
		if err == nil {
			_, err = client.GetInfo()
		}
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("devnet role %q never became ready: %v", role, lastErr)
}

// MineBlocks mines n regtest blocks via lokid's generate RPC (no container
// restart — restarting `chain` wipes its regtest DB, see
// test/devnet/README.md).
func (d *Devnet) MineBlocks(n int) error {
	cmd := exec.Command("docker", "compose", "exec", "-T", "chain", "lokid-cli",
		"--regtest", "--notls", "--rpcuser=devnet", "--rpcpass=devnet", "--rpcserver=localhost:25216",
		"generate", strconv.Itoa(n))
	cmd.Dir = d.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mining %d blocks: %w\n%s", n, err, out)
	}
	return nil
}
