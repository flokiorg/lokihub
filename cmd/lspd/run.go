package main

import (
	"context"
	"encoding/hex"
	"fmt"

	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/flokiorg/flnd/funding"
	"github.com/nbd-wtf/go-nostr"
	"github.com/rs/zerolog/log"

	ecies "github.com/ecies/go/v2"
	btcec "github.com/flokiorg/go-flokicoin/crypto"
	"github.com/flokiorg/lokihub/lspd/adapters/lnd"
	"github.com/flokiorg/lokihub/lspd/adapters/mempool"
	"github.com/flokiorg/lokihub/lspd/adapters/postgres"
	"github.com/flokiorg/lokihub/lspd/chain"
	"github.com/flokiorg/lokihub/lspd/common"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/dashboard"
	"github.com/flokiorg/lokihub/lspd/history"
	"github.com/flokiorg/lokihub/lspd/interceptor"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/flokiorg/lokihub/lspd/notifications"
	"github.com/flokiorg/lokihub/lspd/platform/build"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
	"github.com/flokiorg/lokihub/lspd/services"
	transdashboard "github.com/flokiorg/lokihub/lspd/transport/dashboard"
	translsps "github.com/flokiorg/lokihub/lspd/transport/lsps"
	"github.com/urfave/cli"
)

// Default configuration values
const (
	DefaultMaxPaymentSizeLoki    = 10_000_000_000 // 10k Loki
	DefaultMinLifetimeBlocks     = 43200          // ~1 month (1 minute blocks)
	DefaultMaxClientToSelfDelay  = 10080          // ~1 week
	DefaultFeeParamsValidity     = time.Hour      // Fee params validity duration
	DefaultLsps1MaxPendingOrders = 50             // Max pending LSPS1 orders per peer
	// DefaultLsps2MaxPendingRegistrations mirrors DefaultLsps1MaxPendingOrders
	// for LSPS2 buy registrations (Phase 7, F-17/T7-7).
	DefaultLsps2MaxPendingRegistrations = 50
	DefaultLsps5MaxWebhooks             = 100 // Max LSPS5 webhooks per client
	// BurstLimiterEvictionInterval bounds how often the in-memory per-peer
	// burst-shaper map is swept for expired entries -- needed so a real
	// Sybil attempt (many fresh peer identities) can't grow it unbounded.
	BurstLimiterEvictionInterval = 5 * time.Minute

	// LSP Policy Limits
	AbsoluteMinChannelCapacityLoki = 20000 // Hard floor for channel size enforced by LSP
)

func runLspd(cliCtx *cli.Context) error {
	// Sanity Checks: Ensure LSP policy complies with Network limits

	// LSP Min Capacity must be >= Network Min Funding
	if AbsoluteMinChannelCapacityLoki < uint64(funding.MinChanFundingSize) {
		return fmt.Errorf("configuration error: AbsoluteMinChannelCapacityLoki (%d) is less than network limit (%d)", AbsoluteMinChannelCapacityLoki, funding.MinChanFundingSize)
	}

	// LSP Min Capacity must be <= Network Max Funding (otherwise we can never open a standard channel)
	if AbsoluteMinChannelCapacityLoki > uint64(funding.MaxFlokicoinFundingAmount) {
		return fmt.Errorf("configuration error: AbsoluteMinChannelCapacityLoki (%d) exceeds standard maximum funding amount (%d)", AbsoluteMinChannelCapacityLoki, funding.MaxFlokicoinFundingAmount)
	}

	// Network Min Funding must be <= Network Max Funding (Basic sanity of external package)
	if funding.MinChanFundingSize > funding.MaxFlokicoinFundingAmount {
		return fmt.Errorf("external consistency error: Network MinChanFundingSize (%d) > MaxFlokicoinFundingAmount (%d)", funding.MinChanFundingSize, funding.MaxFlokicoinFundingAmount)
	}
	logging.InitLogger()
	log.Info().Msgf(`Starting lspd, tag='%s', revision='%s'`, build.GetTag(), build.GetRevision())

	nodeConfigs, err := loadNodeConfigs()
	if err != nil {
		log.Fatal().Msgf("failed to load nodes config: %v", err)
	}

	announceRelays, err := checkAnnounceRequirements(nodeConfigs, os.Getenv(announceRelaysEnv))
	if err != nil {
		log.Fatal().Msgf("NIP-LSP announcing: %v", err)
	}

	nodes, err := initializeNodes(nodeConfigs)
	if err != nil {
		log.Fatal().Msgf("failed to initialize nodes: %v", err)
	}

	nodesService, err := services.NewNodesService(nodes)
	if err != nil {
		log.Fatal().Msgf("failed to create nodes service: %v", err)
	}

	var feeStrategy chain.FeeStrategy
	envFeeStrategy := os.Getenv("MEMPOOL_PRIORITY")
	switch strings.ToLower(envFeeStrategy) {
	case "minimum":
		feeStrategy = chain.FeeStrategyMinimum
	case "economy":
		feeStrategy = chain.FeeStrategyEconomy
	case "hour":
		feeStrategy = chain.FeeStrategyHour
	case "halfhour":
		feeStrategy = chain.FeeStrategyHalfHour
	case "fastest":
		feeStrategy = chain.FeeStrategyFastest
	default:
		feeStrategy = chain.FeeStrategyEconomy
	}

	var mempoolClient *mempool.MempoolClient
	mempoolUrl := os.Getenv("MEMPOOL_API_BASE_URL")
	if mempoolUrl != "" {
		mempoolClient, err = mempool.NewMempoolClient(mempoolUrl)
		if err != nil {
			log.Fatal().Msgf("failed to initialize mempool client: %v", err)
		}

		log.Info().Msgf("using mempool api for fee estimation: %v, fee strategy: %v:%v", mempoolUrl, envFeeStrategy, feeStrategy)
	}

	databaseUrl := os.Getenv("DATABASE_URL")
	automigrate, _ := strconv.ParseBool(os.Getenv("AUTO_MIGRATE_DATABASE"))
	if automigrate {
		err = postgres.Migrate(databaseUrl)
		if err != nil {
			log.Fatal().Msgf("Failed to migrate postgres database: %v", err)
		}
	}
	pool, err := postgres.PgConnect(databaseUrl)
	if err != nil {
		log.Fatal().Msgf("pgConnect() error: %v", err)
	}

	interceptStore := postgres.NewPostgresInterceptStore(pool)
	openingStore := postgres.NewPostgresOpeningStore(pool)
	notificationsStore := postgres.NewNotificationsStore(pool)
	historyStore := postgres.NewHistoryStore(pool)

	// Logic: We no longer auto-adjust values at runtime. Instead, we validated
	// them above to ensure they are safe based on the configured padding.
	for _, node := range nodes {
		cfg := node.NodeConfig.Load()
		for _, token := range node.Tokens {
			minPaymentSize := cfg.MinPaymentSizeMloki
			maxPaymentSize := cfg.JitMaxPaymentSizeMloki
			if maxPaymentSize == 0 {
				maxPaymentSize = DefaultMaxPaymentSizeLoki
			}

			// Proportional from ChannelFeePermyriad (in basis points of basis points)
			// ChannelFeePermyriad is per 10,000. PPM is per 1,000,000. So multiply by 100.
			proportional := uint32(cfg.ChannelFeePermyriad * 100)

			params := core.OpeningFeeParams{
				MinFeeMloki:          uint64(cfg.ChannelMinimumFeeMloki),
				Proportional:         proportional,
				ValidUntil:           "",
				MinLifetime:          DefaultMinLifetimeBlocks,
				MaxClientToSelfDelay: DefaultMaxClientToSelfDelay,
				MinPaymentSizeMloki:  minPaymentSize,
				MaxPaymentSizeMloki:  maxPaymentSize,
				Promise:              "sync_from_config",
			}

			// Upsert for 1 hour validity
			err := openingStore.SetFeeParams(token, DefaultFeeParamsValidity, params)
			if err != nil {
				log.Error().Msgf("Failed to sync fee params for token %s: %v", token, err)
			} else {
				log.Info().Msgf("Synced fee params for token %s: min_size=%d, max_size=%d", token, minPaymentSize, maxPaymentSize)
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	notificationService := notifications.NewNotificationService(openingStore, nodesService, notificationsStore)

	// Development/test only: admit plain-http LSPS5 webhooks on localhost
	// and let delivery reach loopback addresses.
	allowLoopbackWebhooks, _ := strconv.ParseBool(os.Getenv("LSPS5_ALLOW_LOOPBACK_WEBHOOKS"))
	if allowLoopbackWebhooks {
		log.Warn().Msg("LSPS5_ALLOW_LOOPBACK_WEBHOOKS is set: loopback webhooks are accepted (development only)")
		notificationService.SetAllowLoopbackWebhooks(true)
	}

	// Configure Nostr transport if private key is provided
	lspNostrPrivKey := os.Getenv("LSP_NOSTR_PRIVATE_KEY")
	if lspNostrPrivKey != "" {
		var err error
		lspNostrPrivKey, err = common.ParsePrivateKey(lspNostrPrivKey)
		if err != nil {
			log.Fatal().Err(err).Msg("Invalid LSP_NOSTR_PRIVATE_KEY")
		}

		nostrTransport := notifications.NewNostrTransport(lspNostrPrivKey, allowLoopbackWebhooks)
		notificationService.AddTransport(nostrTransport)
		log.Info().Msg("Nostr notification transport enabled")
	}

	go notificationService.Start(ctx)
	openingService := services.NewOpeningService(openingStore, nodesService)

	// Initialize Dashboard Service
	dashboardAdminPubkey := os.Getenv("DASHBOARD_ADMIN_PUBKEY")
	dashboardListenAddr := os.Getenv("DASHBOARD_LISTEN_ADDRESS")
	if dashboardListenAddr == "" {
		dashboardListenAddr = ":8080"
	}

	dashHandlers := dashboard.NewHandlers(
		nodesService,
		openingService,
		openingStore,
		historyStore,
		os.Getenv("NODES_CONFIG_PATH"),
	)
	dashHandlers.SetAnnounce(announceRelays, func(cfg *config.NodeConfig) error {
		return checkNodeAnnounceRequirements(cfg, announceRelays)
	})
	for _, cfg := range nodeConfigs {
		if cfg.Announcing() {
			log.Info().Msgf("NIP-LSP announcing on for %s, relays %v", nodeLabel(cfg), announceRelays)
		}
	}
	// NIP-LSP announcements (T8-3/T8-4). Nodes join below, once their LSPS
	// handlers exist; a node publishes only while its announce block is on.
	announceStateStore := postgres.NewAnnounceStateStore(pool)
	announcePool := notifications.NewRelayPool()
	announceSvc := services.NewAnnounceService(services.AnnounceServiceConfig{
		Relays:    announceRelays,
		Store:     announceStateStore,
		Publisher: announcePool,
		RelayList: announcePool,
	}, nil)
	go announceSvc.Start(ctx)
	// Pause/Resume/Unlist (T8-2) republish at once.
	dashHandlers.SetAnnounceState(announceStateStore, func(st *core.AnnounceState) {
		log.Info().Msgf("NIP-LSP announcement status of %x is %s", st.NodeID, st.Status)
		announceSvc.Trigger()
	})
	dashHandlers.SetAnnouncePublisher(announceSvc)
	dashSvc := transdashboard.NewServer(dashboardAdminPubkey, dashHandlers, nodesService)

	// Start dashboard HTTP server
	go func() {
		log.Info().Msgf("Starting dashboard server on %s", dashboardListenAddr)
		if err := transdashboard.NewHTTPServer(dashboardListenAddr, dashSvc).ListenAndServe(); err != nil {
			log.Error().Err(err).Msg("Dashboard server failed")
		}
	}()

	// Use bridge for notifications to update dashboard in real-time
	dashBridge := NewDashboardNotificationBridge(notificationService, dashSvc)
	notificationCleanupService := notifications.NewCleanupService(notificationsStore)
	go notificationCleanupService.Start(ctx)
	orderCleanupService := lsps.NewOrderCleanupService(openingStore, 24*time.Hour)
	go orderCleanupService.Start()
	channelSync := history.NewChannelSync(nodes, historyStore)
	go channelSync.ChannelsSynchronize(ctx)

	var settlementServices []*lsps.Lsps1SettlementService
	var interceptors []interceptor.HtlcInterceptor
	var lspsServers []*translsps.Server
	for _, node := range nodes {
		nodeCfg := node.NodeConfig.Load()
		var htlcInterceptor interceptor.HtlcInterceptor
		var feeEstimator chain.FeeEstimator
		if mempoolClient == nil {
			feeEstimator = chain.NewDefaultFeeEstimator(nodeCfg.TargetConf)
		} else {
			feeEstimator = mempoolClient
		}

		if nodeCfg.Lnd == nil {
			log.Fatal().Msgf("node has to be flnd")
		}

		client, err := lnd.NewLndClient(nodeCfg.Lnd)
		if err != nil {
			log.Fatal().Msgf("failed to initialize LND client: %v", err)
		}

		client.StartListeners()

		// Start LSPS1 settlement service for this node
		settlementSvc := lsps.NewLsps1SettlementService(client, openingStore, historyStore, node.NodeConfig, feeEstimator, feeStrategy, dashBridge)
		// orderWatcher is only wired into lspsHandlers below if the
		// settlement service actually started (H-12) -- mirrors the
		// existing behavior where a failed settlement-service start
		// already leaves this node's invoice-driven processing disabled.
		var orderWatcher lsps.OrderWatcher
		err = settlementSvc.Start(ctx)
		if err != nil {
			log.Error().Err(err).Msg("Failed to start LSPS1 settlement service")
		} else {
			settlementServices = append(settlementServices, settlementSvc)
			orderWatcher = settlementSvc
		}

		forwardSync := lnd.NewForwardSync(node.NodeId, client, historyStore)
		go forwardSync.ForwardsSynchronize(ctx)
		notificationService.SetNodePrivKey(node.PrivateKey)
		handler := interceptor.NewInterceptHandler(client, node.NodeConfig, interceptStore, historyStore, openingService, feeEstimator, feeStrategy, dashBridge, node.PrivateKey)
		// Phase 7 (T7-13): reconcile any LSPS2 registration left stuck at
		// OPENING by a crash between openChannel succeeding and the
		// consuming DB write. Launched in a goroutine, mirroring LSPS1's
		// own recoverStuckOrders (settlement_service.go: "Recover stuck
		// orders in a separate goroutine to avoid blocking Start()") --
		// this must NOT block startup (with multiple nodes, a slow
		// ListChannels call here would delay every later node's init
		// too). Safe to run concurrently with the interceptor already
		// taking live traffic: ClaimLsps2Registration's CAS
		// (state='ACTIVE') already fails closed against any registration
		// still at OPENING, so a real HTLC racing this sweep is simply
		// rejected and retried later, never double-processed.
		go handler.RecoverStuckLsps2Registrations()
		combinedHandler := services.NewCombinedHandler(handler)
		htlcInterceptor, err = lnd.NewLndHtlcInterceptor(node.NodeConfig, client, combinedHandler)
		if err != nil {
			log.Fatal().Msgf("failed to initialize LND interceptor: %v", err)
		}

		interceptors = append(interceptors, htlcInterceptor)

		lsps1MaxOrders, _ := strconv.Atoi(os.Getenv("LSPS1_MAX_PENDING_ORDERS"))
		if lsps1MaxOrders <= 0 {
			lsps1MaxOrders = DefaultLsps1MaxPendingOrders
		}
		lsps5MaxWebhooks, _ := strconv.Atoi(os.Getenv("LSPS5_MAX_WEBHOOKS"))
		if lsps5MaxWebhooks <= 0 {
			lsps5MaxWebhooks = DefaultLsps5MaxWebhooks
		}
		lsps2MaxRegistrations, _ := strconv.Atoi(os.Getenv("LSPS2_MAX_PENDING_REGISTRATIONS"))
		if lsps2MaxRegistrations <= 0 {
			lsps2MaxRegistrations = DefaultLsps2MaxPendingRegistrations
		}
		// Unlike the per-peer standing caps above (always on, with a real
		// default), the fleet-wide ceiling is opt-in: zero means disabled,
		// so an existing deployment that never sets this env var sees no
		// behavior change (Phase 7, F-17/T7-7).
		maxGlobalPendingReservationsLoki, _ := strconv.ParseUint(os.Getenv("MAX_GLOBAL_PENDING_RESERVATIONS_LOKI"), 10, 64)

		var lspNostrPubkey string
		if lspNostrPrivKey != "" {
			var err error
			lspNostrPubkey, err = nostr.GetPublicKey(lspNostrPrivKey)
			if err != nil {
				log.Error().Err(err).Msg("Failed to derive Nostr public key")
			}
		}

		lspsConfig := lsps.Config{
			Lsps1MaxPendingOrders:               lsps1MaxOrders,
			Lsps2MaxPendingRegistrations:        lsps2MaxRegistrations,
			MaxGlobalPendingReservationsPerNode: maxGlobalPendingReservationsLoki,
			Lsps5MaxWebhooks:                    lsps5MaxWebhooks,
			EnableLsps5Https:                    true,
			EnableLsps5Nostr:                    lspNostrPrivKey != "",
			Lsps5NostrPubkey:                    lspNostrPubkey,
			Lsps5AllowLoopbackWebhooks:          allowLoopbackWebhooks,
		}

		liquidityManager := lsps.NewLiquidityManager(client, openingStore)
		lspsHandlers, err := newNodeLSPSHandlers(node, lspsConfig, client, openingService, liquidityManager, dashBridge, orderWatcher)
		if err != nil {
			log.Fatal().Err(err).Msgf("LSPS handlers for %s", nodeLabel(nodeCfg))
		}
		go lspsHandlers.BurstLimiter().StartEvictionLoop(ctx.Done(), BurstLimiterEvictionInterval)
		go lspsHandlers.ReadLimiter().StartEvictionLoop(ctx.Done(), BurstLimiterEvictionInterval)
		if signer, err := services.NewAnnouncementSigner(node); err != nil {
			log.Error().Err(err).Msg("NIP-LSP: no announcement signer for node; it cannot announce")
		} else {
			announceSvc.AddNode(services.AnnounceNode{
				Node:    node,
				Signer:  signer,
				Answers: lspsHandlers,
				Chain:   node.Chain,
				Network: node.Network,
			})
		}
		lspsServer := translsps.NewServer(client, lspsHandlers)
		lspsServer.Start()
		lspsServers = append(lspsServers, lspsServer)
	}

	address := os.Getenv("LISTEN_ADDRESS")
	certMagicDomain := os.Getenv("CERTMAGIC_DOMAIN")
	cs := NewChannelOpenerServer(interceptStore, openingService)
	ns := notifications.NewNotificationsServer(notificationsStore)
	s, err := NewGrpcServer(nodesService, address, certMagicDomain, cs, ns)
	if err != nil {
		log.Fatal().Msgf("failed to initialize grpc server: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(len(interceptors) + 1)

	// stopInterceptors is invoked concurrently from up to three independent
	// paths: each interceptor's own goroutine when it stops, the gRPC
	// server's goroutine when it stops, and the SIGTERM/SIGINT handler.
	// Any two of those racing (e.g. the SIGTERM handler stopping the gRPC
	// server, which its own goroutine then observes and reacts to) means
	// this runs more than once — services like OrderCleanupService aren't
	// safe to Stop() twice (double-close panics), so gate the whole thing
	// behind sync.Once to make shutdown idempotent regardless of which
	// path gets there first.
	var stopOnce sync.Once
	stopInterceptors := func() {
		stopOnce.Do(func() {
			for _, interceptor := range interceptors {
				if err := interceptor.Stop(); err != nil {
					log.Error().Err(err).Msg("Failed to stop interceptor")
				}
			}

			for _, lspsServer := range lspsServers {
				lspsServer.Stop()
			}

			for _, settlementSvc := range settlementServices {
				settlementSvc.Stop()
			}

			orderCleanupService.Stop()

			cancel()
		})
	}

	for _, interceptor := range interceptors {
		i := interceptor
		go func() {
			err := i.Start()
			if err == nil {
				log.Info().Msgf("Interceptor stopped.")

			} else {
				log.Error().Msgf("FATAL. Interceptor stopped with error: %v", err)
			}

			wg.Done()

			// If any interceptor stops, stop everything, so we're able to restart using systemd.
			s.Stop()
			stopInterceptors()
		}()
	}

	go func() {
		err := s.Start()
		if err == nil {
			log.Info().Msgf("GRPC server stopped.")
		} else {
			log.Error().Msgf("FATAL. GRPC server stopped with error: %v", err)
		}

		wg.Done()

		// If the server stops, stop everything else, so we're able to restart using systemd.
		stopInterceptors()
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-c
		log.Info().Msgf("Received stop signal %v. Stopping.", sig)

		// Stop everything gracefully on stop signal
		s.Stop()
		stopInterceptors()
	}()

	wg.Wait()
	log.Info().Msgf("lspd exited")
	return nil
}

func initializeNodes(configs []*config.NodeConfig) ([]*core.Node, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("no nodes supplied")
	}

	nodes := []*core.Node{}
	// Named cfg, not config, so the config package import stays reachable
	// inside the loop body (needed below to construct the NodeConfigRef).
	for _, cfg := range configs {
		flspdPrivateKey, err := cfg.FlspdPrivateKey.Resolve()
		if err != nil {
			return nil, fmt.Errorf("resolving config.flspdPrivateKey: %w", err)
		}

		hexKey, err := common.ParsePrivateKey(flspdPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("ParsePrivateKey(config.flspdPrivateKey) error: %v", err)
		}

		pk, err := hex.DecodeString(hexKey)
		if err != nil {
			return nil, fmt.Errorf("hex.DecodeString error: %v", err)
		}

		eciesPrivateKey := ecies.NewPrivateKeyFromBytes(pk)
		eciesPublicKey := eciesPrivateKey.PublicKey
		privateKey, publicKey := btcec.PrivKeyFromBytes(pk)

		// Validate config using the config package validation logic
		if err := cfg.Validate(); err != nil {
			return nil, err
		}

		// Validate wumbo limits (PublicChannelAmount or explicit wumbo needs)
		if cfg.PublicChannelAmount > int64(funding.MaxFlokicoinFundingAmountWumbo) {
			return nil, fmt.Errorf("publicChannelAmount %d exceeds MaxFlokicoinFundingAmountWumbo %d", cfg.PublicChannelAmount, funding.MaxFlokicoinFundingAmountWumbo)
		}
		// Warn if exceeding standard limit but under wumbo
		if cfg.PublicChannelAmount > int64(funding.MaxFlokicoinFundingAmount) {
			log.Warn().Msgf("PublicChannelAmount %d exceeds standard MaxFlokicoinFundingAmount %d. Ensure wumbo is enabled.", cfg.PublicChannelAmount, funding.MaxFlokicoinFundingAmount)
		}

		if err := cfg.ValidateLsps1(); err != nil {
			return nil, fmt.Errorf("LSPS1 config validation failed: %w", err)
		}

		if err := cfg.ValidateJit(); err != nil {
			return nil, fmt.Errorf("JIT config validation failed: %w", err)
		}

		if cfg.Lnd == nil {
			return nil, fmt.Errorf("node has to be flnd")
		}

		client, err := lnd.NewLndClient(cfg.Lnd)
		if err != nil {
			return nil, err
		}

		// Make sure the nodes is available and set name and pubkey if not
		// set in config. This mutates cfg directly, before it's wrapped in
		// a NodeConfigRef below — safe, single-threaded startup code.
		//
		// Retried for a bit: on a fresh `docker compose up`, flspd's own
		// container can win the race against flnd's gRPC server still
		// coming up, and a single failed attempt here would otherwise take
		// down the whole process (fatal below) for a purely transient
		// reason.
		info, err := getInfoWithRetry(client, 30*time.Second)
		if err != nil {
			decodedPubkey, decodeErr := hex.DecodeString(cfg.NodePubkey)
			if cfg.Name == "" || cfg.NodePubkey == "" ||
				decodeErr != nil || len(decodedPubkey) != 33 {

				return nil, fmt.Errorf("failed to get info from host %s", cfg.Host)
			}

			info = &lightning.GetInfoResult{
				Alias:  cfg.Name,
				Pubkey: cfg.NodePubkey,
			}
		}

		if cfg.Name == "" {
			cfg.Name = info.Alias
		}

		if cfg.NodePubkey == "" {
			cfg.NodePubkey = info.Pubkey
		}

		nodeId, err := hex.DecodeString(info.Pubkey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse node id %s", info.Pubkey)
		}

		node := &core.Node{
			NodeConfig:      config.NewNodeConfigRef(cfg),
			PrivateKey:      privateKey,
			PublicKey:       publicKey,
			EciesPrivateKey: eciesPrivateKey,
			EciesPublicKey:  eciesPublicKey,
			Client:          client,
			NodeKeySigner:   client,
			NodeId:          nodeId,
			Tokens:          cfg.Tokens,
			Chain:           info.Chain,
			Network:         info.Network,
		}
		nodes = append(nodes, node)

		log.Info().Msgf("LSPS Connection URI: %s@%s", info.Pubkey, cfg.Host)
	}

	return nodes, nil
}

// getInfoWithRetry retries client.GetInfo() with a fixed backoff until it
// succeeds or timeout elapses, returning the last error on timeout.
func getInfoWithRetry(client *lnd.LndClient, timeout time.Duration) (*lightning.GetInfoResult, error) {
	deadline := time.Now().Add(timeout)
	const retryInterval = 2 * time.Second

	for {
		info, err := client.GetInfo()
		if err == nil {
			return info, nil
		}

		if time.Now().After(deadline) {
			return nil, err
		}

		log.Warn().Err(err).Msg("GetInfo failed, node may still be starting up, retrying...")
		time.Sleep(retryInterval)
	}
}
