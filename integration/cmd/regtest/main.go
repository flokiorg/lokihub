// Command regtest bootstraps and drives the regtest dev stack (docker-compose.dev.yml):
//
//	regtest certs                     write the chain node's RPC cert (valid for chain and DEV_HOST)
//	regtest bootstrap                 fund the nodes, open channels (flnd-lsp's too), set lokihub up, write the test config
//	regtest mine [N] [address]        mine N blocks (default 1) to the payer, or to address
//	regtest fund <hub|payer|address> <loki>   pay from the payer's wallet, then mine 1
//	regtest channels                  reopen either channel if it is missing
//	regtest status                    tip, wallet and channel balances, lokihub state
//	regtest flspd-admin-key           make flspd's dashboard admin key once; print its pubkey
//
// It reads the stack's ports and paths from the environment (the repo's .env), and
// refuses any chain or node that is not on regtest.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/integration/regtest"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type settings struct {
	host, dataDir, relay, password, configPath string
	chain                                      regtest.ChainConfig
	hub, payer, lsp                            regtest.NodeConfig
	lokihubURL                                 string
}

func load() settings {
	host := env("DEV_HOST", "localhost")
	data := env("DEV_REGTEST_DATA_DIR", filepath.Join("data", "regtest"))
	return settings{
		host:       host,
		dataDir:    data,
		relay:      env("RELAY", "ws://relay:5500"),
		password:   env("DEV_UNLOCK_PASSWORD", ""),
		configPath: env("INTEGRATION_CONFIG", filepath.Join("integration", "config.local.yaml")),
		chain: regtest.ChainConfig{
			Host:     host + ":" + env("CHAIN_RPC_PORT", "25213"),
			User:     "dev",
			Pass:     "dev",
			CertPath: filepath.Join(data, "chain", "rpc.cert"),
		},
		hub: regtest.NodeConfig{
			Name: "hub", Address: host + ":" + env("FLND_RPC_PORT", "10005"),
			Dir: filepath.Join(data, "flnd"), P2P: "flnd:9735",
		},
		payer: regtest.NodeConfig{
			Name: "payer", Address: host + ":" + env("PAYER_RPC_PORT", "10006"),
			Dir: filepath.Join(data, "payer"), P2P: "payer:9735",
		},
		// flspd's node, in the compose "lsp" profile: dialled only when its data
		// directory holds a macaroon.
		lsp: regtest.NodeConfig{
			Name: "lsp", Address: host + ":" + env("LSP_RPC_PORT", "10007"),
			Dir: filepath.Join(data, "flnd-lsp"), P2P: "flnd-lsp:9735",
		},
		lokihubURL: env("LOKIHUB_URL", "http://"+host+":"+env("CADDY_PORT", "5610")),
	}
}

func dial(ctx context.Context, s settings) (*regtest.Stack, error) {
	chain, err := regtest.DialChain(s.chain)
	if err != nil {
		return nil, err
	}
	st := &regtest.Stack{Chain: chain, Lokihub: regtest.NewLokihub(s.lokihubURL), Logf: logf}
	for _, n := range []struct {
		cfg regtest.NodeConfig
		dst **regtest.Node
	}{{s.hub, &st.Hub}, {s.payer, &st.Payer}} {
		// A node's wallet and macaroon appear a little after the container starts.
		deadline := time.Now().Add(2 * time.Minute)
		for {
			node, err := regtest.DialNode(ctx, n.cfg)
			if err == nil {
				*n.dst = node
				break
			}
			if time.Now().After(deadline) || errorsIsNotRegtest(err) {
				st.Close()
				return nil, err
			}
			time.Sleep(2 * time.Second)
		}
	}
	if _, statErr := os.Stat(regtest.MacaroonPath(s.lsp.Dir)); statErr == nil {
		deadline := time.Now().Add(2 * time.Minute)
		for {
			node, err := regtest.DialNode(ctx, s.lsp)
			if err == nil {
				st.Lsp = node
				break
			}
			if time.Now().After(deadline) || errorsIsNotRegtest(err) {
				st.Close()
				return nil, err
			}
			time.Sleep(2 * time.Second)
		}
	}
	tip, err := st.Chain.Tip()
	if err == nil {
		heights := []int32{st.Hub.Height, st.Payer.Height}
		if st.Lsp != nil {
			heights = append(heights, st.Lsp.Height)
		}
		err = regtest.CheckChainNotReset(tip, heights...)
	}
	if err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

func errorsIsNotRegtest(err error) bool {
	return errors.Is(err, regtest.ErrNotRegtest)
}

func logf(format string, args ...any) { fmt.Printf("regtest: "+format+"\n", args...) }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "regtest:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: regtest certs|bootstrap|mine|fund|channels|status|flspd-admin-key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	s := load()
	if args[0] == "certs" {
		// Before the chain node first starts: it then uses this cert instead of its own.
		return regtest.WriteChainCerts(filepath.Join(s.dataDir, "chain"), []string{"chain", s.host})
	}
	if args[0] == "flspd-admin-key" {
		return flspdAdminKey(filepath.Join(s.dataDir, "flspd-admin", "admin.key"))
	}
	st, err := dial(ctx, s)
	if err != nil {
		return err
	}
	defer st.Close()

	switch args[0] {
	case "bootstrap":
		return bootstrap(ctx, st, s)
	case "mine":
		n, to := 1, "payer"
		if len(args) > 1 {
			if n, err = strconv.Atoi(args[1]); err != nil || n < 1 {
				return fmt.Errorf("mine: N must be a positive number")
			}
		}
		if len(args) > 2 {
			to = args[2]
		}
		return st.MineTo(ctx, n, to)
	case "fund":
		if len(args) != 3 {
			return fmt.Errorf("usage: regtest fund <hub|payer|address> <loki>")
		}
		amount, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil || amount <= 0 {
			return fmt.Errorf("fund: amount must be a positive number of loki")
		}
		return st.Fund(ctx, args[1], amount)
	case "channels":
		return st.EnsureChannels(ctx)
	case "status":
		return status(ctx, st)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func bootstrap(ctx context.Context, st *regtest.Stack, s settings) error {
	if s.password == "" {
		return fmt.Errorf("DEV_UNLOCK_PASSWORD is not set (see .env.example)")
	}
	// flnd counts itself synced only while the tip is under two hours old.
	if err := st.MineTo(ctx, 1, "payer"); err != nil {
		return err
	}
	tip, err := st.Chain.Tip()
	if err != nil {
		return err
	}
	for _, step := range regtest.FundingPlan(tip) {
		logf("mining %d blocks to the %s", step.N, step.To)
		if err := st.MineTo(ctx, step.N, step.To); err != nil {
			return err
		}
	}
	if err := st.EnsureChannels(ctx); err != nil {
		return err
	}
	if st.Lsp != nil {
		// flspd opens LSPS1 and JIT channels from flnd-lsp's wallet, and a JIT
		// payment from the payer reaches the Hub through flnd-lsp.
		if err := st.EnsureLSP(ctx); err != nil {
			return err
		}
	} else {
		logf("no flnd-lsp (the flspd image is not built): the LSP catalogue tests skip")
	}
	token, err := st.SetupLokihub(ctx, s.password, s.relay)
	if err != nil {
		return err
	}
	// The node switch of on-chain withdrawals (a backend without them answers
	// 404, which is fine): the Hub node's wallet is funded above, so its Cash
	// Hubs can offer them once their terms are on.
	if err := st.Lokihub.EnableWithdrawals(ctx, token); err != nil {
		logf("on-chain withdrawals not switched on: %v", err)
	}
	var cfg regtest.IntegrationConfig
	cfg.AdminAPI.BaseURL = s.lokihubURL
	cfg.AdminAPI.Token = token
	cfg.RelayURL = s.relay
	cfg.Regtest = regtest.RegtestConfig{
		ChainRPC: s.chain.Host, ChainRPCUser: s.chain.User, ChainRPCPass: s.chain.Pass,
		ChainRPCCert:  abs(s.chain.CertPath),
		PayerGRPC:     s.payer.Address,
		PayerMacaroon: abs(regtest.MacaroonPath(s.payer.Dir)),
		PayerTLSCert:  abs(filepath.Join(s.payer.Dir, "tls.cert")),
	}
	if err := regtest.WriteConfig(s.configPath, cfg); err != nil {
		return err
	}
	logf("wrote %s", s.configPath)
	return status(ctx, st)
}

func abs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func status(ctx context.Context, st *regtest.Stack) error {
	tip, err := st.Chain.Tip()
	if err != nil {
		return err
	}
	fmt.Printf("tip height        %d\n", tip)
	nodes := []*regtest.Node{st.Hub, st.Payer}
	if st.Lsp != nil {
		nodes = append(nodes, st.Lsp)
	}
	for _, n := range nodes {
		bal, err := n.RPC.WalletBalance(ctx, &lnrpc.WalletBalanceRequest{})
		if err != nil {
			return err
		}
		ch, err := n.RPC.ChannelBalance(ctx, &lnrpc.ChannelBalanceRequest{})
		if err != nil {
			return err
		}
		var local, remote uint64
		if ch.LocalBalance != nil {
			local = ch.LocalBalance.Sat
		}
		if ch.RemoteBalance != nil {
			remote = ch.RemoteBalance.Sat
		}
		fmt.Printf("%-6s on-chain   %d loki confirmed\n", n.Config.Name, bal.ConfirmedBalance)
		fmt.Printf("%-6s channels   %d loki out, %d loki in\n", n.Config.Name, local, remote)
	}
	info, err := st.Lokihub.Info(ctx)
	if err != nil {
		fmt.Printf("lokihub           unreachable (%v)\n", err)
		return nil
	}
	fmt.Printf("lokihub           setup=%t running=%t network=%s\n", info.SetupCompleted, info.Running, info.Network)
	return nil
}

// flspdAdminKey makes the key that logs in to flspd's dashboard (NIP-98) once,
// at path, and prints its pubkey for flspd's DASHBOARD_ADMIN_PUBKEY. The
// backend's flspd test hooks sign with it (LOKIHUB_TEST_FLSPD_ADMIN_KEY_FILE).
func flspdAdminKey(path string) error {
	raw, err := os.ReadFile(path) //nolint:gosec // the stack's own data dir
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		raw = []byte(nostr.GeneratePrivateKey())
		if err := os.WriteFile(path, raw, 0o644); err != nil { //nolint:gosec // read by the backend container; a regtest throwaway
			return err
		}
	} else if err != nil {
		return err
	}
	pub, err := nostr.GetPublicKey(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	fmt.Println(pub)
	return nil
}
