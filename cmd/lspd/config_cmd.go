package main

import (
	"fmt"
	"os"

	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/urfave/cli"
	"gopkg.in/yaml.v3"
)

// T5-6: operator visibility tooling for the nodes.yaml configuration —
// `flspd config check`, `flspd config show --effective`, and
// `flspd config explain fees`. All three load configuration the same way
// the daemon itself does (loadNodeConfigs) and never connect to Postgres
// or flnd, so they're safe to run against a config file before (or
// without) bringing the rest of flspd up.
var configCommand = cli.Command{
	Name:  "config",
	Usage: "Inspect and validate lspd's node configuration.",
	Subcommands: []cli.Command{
		configCheckCommand,
		configShowCommand,
		configExplainCommand,
	},
}

var nodeIndexFlag = cli.IntFlag{
	Name:  "node-index",
	Usage: "Which configured node to operate on, if nodes.yaml has more than one.",
	Value: 0,
}

var configCheckCommand = cli.Command{
	Name:  "check",
	Usage: "Validate the configured nodes and report the first error found, if any.",
	Action: func(cliCtx *cli.Context) error {
		configs, err := loadNodeConfigs()
		if err != nil {
			return err
		}
		for i, cfg := range configs {
			if err := cfg.Validate(); err != nil {
				return fmt.Errorf("node %d (%s): %w", i, nodeLabel(cfg), err)
			}
			// Validate() only checks business-rule fields — it doesn't
			// touch FlspdPrivateKey/Lnd.Cert/Lnd.Macaroon at all (see
			// SecretRef's doc comment for why resolution is deliberately
			// not automatic). Resolve them here too, since "is my
			// nodes.yaml actually usable" is exactly what an operator
			// runs `config check` to find out, and a missing env var or
			// unreadable file would otherwise only surface once the
			// daemon itself starts.
			if _, err := cfg.FlspdPrivateKey.Resolve(); err != nil {
				return fmt.Errorf("node %d (%s): flspdPrivateKey: %w", i, nodeLabel(cfg), err)
			}
			if cfg.Lnd != nil {
				if _, err := cfg.Lnd.Cert.Resolve(); err != nil {
					return fmt.Errorf("node %d (%s): flnd.cert: %w", i, nodeLabel(cfg), err)
				}
				if _, err := cfg.Lnd.Macaroon.Resolve(); err != nil {
					return fmt.Errorf("node %d (%s): flnd.macaroon: %w", i, nodeLabel(cfg), err)
				}
			}
		}
		if _, err := checkAnnounceRequirements(configs, os.Getenv(announceRelaysEnv)); err != nil {
			return err
		}
		_, err = fmt.Fprintf(os.Stdout, "OK: %d node(s) configured, all valid.\n", len(configs))
		return err
	},
}

var configShowCommand = cli.Command{
	Name:  "show",
	Usage: "Print the fully-resolved configuration (tiered blocks and profile presets expanded).",
	Flags: []cli.Flag{nodeIndexFlag},
	Action: func(cliCtx *cli.Context) error {
		cfg, err := selectedNodeConfig(cliCtx)
		if err != nil {
			return err
		}
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("config is invalid, showing it anyway may be misleading: %w", err)
		}

		// SecretRef's own MarshalYAML re-emits the from_env/from_file
		// reference (or the inline value, if that's how it was
		// configured) rather than resolving it — so this never prints a
		// raw secret, without any extra stripping logic needed here.
		// "Effective" refers to the business-logic fields: every
		// tiered-block expansion and profile default is fully applied by
		// Validate() above, so what's printed is what flspd would
		// actually use, not just what's written in nodes.yaml.
		out, err := yaml.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("marshaling effective config: %w", err)
		}
		_, err = fmt.Fprint(os.Stdout, string(out))
		return err
	},
}

var configExplainCommand = cli.Command{
	Name:  "explain",
	Usage: "Explain a piece of the effective configuration in plain language.",
	Subcommands: []cli.Command{
		configExplainFeesCommand,
	},
}

var configExplainFeesCommand = cli.Command{
	Name:  "fees",
	Usage: "Show what the channel-opening fee would be for a given payment size.",
	Flags: []cli.Flag{
		nodeIndexFlag,
		cli.Uint64Flag{
			Name:     "payment-loki",
			Usage:    "Payment size, in loki, to compute the opening fee for.",
			Required: true,
		},
	},
	Action: func(cliCtx *cli.Context) error {
		cfg, err := selectedNodeConfig(cliCtx)
		if err != nil {
			return err
		}
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("config is invalid: %w", err)
		}

		paymentLoki := cliCtx.Uint64("payment-loki")
		minFeeMloki := uint64(cfg.ChannelMinimumFeeMloki)
		// ChannelFeePermyriad is per-10,000; ComputeOpeningFee's
		// proportional parameter is parts-per-million (per-1,000,000).
		proportional := uint64(cfg.ChannelFeePermyriad) * 100

		feeMloki, err := lsps.ComputeOpeningFee(paymentLoki*1000, minFeeMloki, proportional)
		if err != nil {
			return fmt.Errorf("computing opening fee: %w", err)
		}
		feeLoki := feeMloki / 1000
		ratePercent := float64(cfg.ChannelFeePermyriad) / 100

		_, err = fmt.Fprintf(os.Stdout,
			"For a payment of %d loki, the opening fee would be %d loki (%.4g%% proportional, %d loki floor).\n",
			paymentLoki, feeLoki, ratePercent, minFeeMloki/1000,
		)
		return err
	},
}

// selectedNodeConfig loads the configured nodes and returns the one
// --node-index refers to (default 0).
func selectedNodeConfig(cliCtx *cli.Context) (*config.NodeConfig, error) {
	configs, err := loadNodeConfigs()
	if err != nil {
		return nil, err
	}
	idx := cliCtx.Int("node-index")
	if idx < 0 || idx >= len(configs) {
		return nil, fmt.Errorf("node-index %d out of range: %d node(s) configured", idx, len(configs))
	}
	return configs[idx], nil
}

// nodeLabel returns a human-readable identifier for a node in CLI output —
// its configured name if set, falling back to its pubkey, since Validate()
// runs before either might otherwise be known.
func nodeLabel(cfg *config.NodeConfig) string {
	if cfg.Name != "" {
		return cfg.Name
	}
	if cfg.NodePubkey != "" {
		return cfg.NodePubkey
	}
	return "(unnamed)"
}
