package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/history"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/lsps"
	"github.com/flokiorg/lokihub/lspd/platform/build"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"github.com/flokiorg/lokihub/lspd/ports"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

const (
	// Dashboard limits and scoring
	radarFullMark        = 150
	defaultListLimit     = 100
	analyticsListLimit   = 2000
	summaryListLimit     = 1000
	analyticsHistoryDays = 14
)

// Handlers implements the dashboard's JSON-RPC methods (get_summary,
// list_orders, update_config, ...): parsing a method's params, doing the
// real work (config CRUD, analytics/liquidity aggregation), and returning
// a result or error. How a request arrived (HTTP, NIP-98 auth, the
// admin-pubkey authorization gate, the websocket event feed) is
// transport/dashboard's job; see Route below for the one seam between
// them.
type Handlers struct {
	nodesService   ports.NodesService
	openingService ports.OpeningService
	openingStore   ports.OpeningStore
	historyStore   history.Store
	configPath     string // Path to nodes.yaml for persistence

	// configMu serializes update_config from building the node list to the
	// in-memory Store, so a save for one node always includes the save for
	// another that finished before it (T8-6, R4-F2).
	configMu sync.Mutex

	// NIP-LSP announcing (T8-2): the relays from NOSTR_ANNOUNCE_RELAYS,
	// shown read-only, and the check a config update that turns announcing
	// on must pass (relays set, macaroon grants signer:generate).
	announceRelays []string
	announceCheck  func(cfg *config.NodeConfig) error

	// Pause/Resume/Unlist state (T8-2) and the hook called after every
	// applied action, changed or not (T8-3's publisher republishes when the
	// status differs from what it last published).
	announceState         ports.AnnounceStateStore
	onAnnounceStateChange func(state *core.AnnounceState)

	// The publisher (T8-3/T8-4): its status is shown, a config update
	// triggers it, and recheck_announcement lifts a self-check block.
	announcePublisher AnnouncePublisher
}

// AnnouncePublisher is what the dashboard needs of the announcement
// publisher.
type AnnouncePublisher interface {
	Status(nodeID []byte) *core.AnnouncePublisherStatus
	Recheck(ctx context.Context, nodeID []byte) error
	Trigger()
}

// SetAnnouncePublisher connects the announcement publisher.
func (s *Handlers) SetAnnouncePublisher(p AnnouncePublisher) {
	s.announcePublisher = p
}

// SetAnnounceState gives the handlers the announcement status store and an
// optional hook called after every applied Pause/Resume/Unlist action.
func (s *Handlers) SetAnnounceState(store ports.AnnounceStateStore, onChange func(state *core.AnnounceState)) {
	s.announceState = store
	s.onAnnounceStateChange = onChange
}

// SetAnnounce gives the handlers the announce relays and the extra check run
// on a config update that turns announcing on. Without it, such an update is
// checked by NodeConfig.Validate only.
func (s *Handlers) SetAnnounce(relays []string, check func(cfg *config.NodeConfig) error) {
	s.announceRelays = append([]string(nil), relays...)
	s.announceCheck = check
}

type SummaryResponse struct {
	NodeName          string         `json:"node_name"`
	NodePubkey        string         `json:"node_pubkey"`
	NodeHost          string         `json:"node_host"`
	Version           string         `json:"version"`
	Revenue24h        uint64         `json:"revenue_24h"`
	LiquidityTotal    uint64         `json:"liquidity_total"`
	LiquidityFree     uint64         `json:"liquidity_free"`
	LiquidityBlocked  uint64         `json:"liquidity_blocked"`
	LiquidityOutbound uint64         `json:"liquidity_outbound"`
	LiquidityInbound  uint64         `json:"liquidity_inbound"`
	ActiveOrders      map[string]int `json:"active_orders"`
	NodeStatus        string         `json:"node_status"`
	SyncedToChain     bool           `json:"synced_to_chain"`
	SyncHeight        uint32         `json:"sync_height"`
	PeersConnected    uint32         `json:"peers_connected"`
	ChannelsActive    uint32         `json:"channels_active"`
	ChannelsPending   uint32         `json:"channels_pending"`
	ChannelsInactive  uint32         `json:"channels_inactive"`
	RevenueTrend      string         `json:"revenue_trend"`
}

func NewHandlers(
	nodesService ports.NodesService,
	openingService ports.OpeningService,
	openingStore ports.OpeningStore,
	historyStore history.Store,
	configPath string,
) *Handlers {
	return &Handlers{
		nodesService:   nodesService,
		openingService: openingService,
		openingStore:   openingStore,
		historyStore:   historyStore,
		configPath:     configPath,
	}
}

// Route dispatches an already-decoded JSON-RPC request to the handler for
// its method, by name. The only seam between transport/dashboard (which
// owns HTTP routing, NIP-98 auth, and JSON-RPC envelope marshal/unmarshal)
// and Handlers (this type): transport/dashboard calls Route once it has a
// *lsps.JsonRpcRequest, and does nothing else with the method name itself.
func (s *Handlers) Route(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	switch req.Method {
	case "get_summary":
		return s.handleGetSummary(req)
	case "list_orders":
		return s.handleListOrders(req)
	case "get_radar_data":
		return s.handleGetRadarData(req)
	case "get_analytics":
		return s.handleGetAnalytics(req)
	case "list_nodes":
		return s.handleListNodes(req)
	case "get_config":
		return s.handleGetConfig(req)
	case "update_config":
		return s.handleUpdateConfig(req)
	case "get_announce_state":
		return s.handleGetAnnounceState(req)
	case "set_announce_state":
		return s.handleSetAnnounceState(req)
	case "recheck_announcement":
		return s.handleRecheckAnnouncement(req)
	case "list_peers":
		return s.handleListPeers(req)
	case "list_jit_channels":
		return s.handleListJitChannels(req)
	case "get_order_detail":
		return s.handleGetOrderDetail(req)
	default:
		return lsps.NewJsonRpcErrorResponse(req.Id, -32601, "Method not found", nil)
	}
}

func (s *Handlers) handleGetRadarData(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	// Calculate real metrics
	totalCapacity, _, reserved := s.getLiquidityMetrics(context.Background())

	// 1. Liquidity Score (0-radarFullMark)
	liquidityScore := radarFullMark
	if totalCapacity > 0 {
		usage := float64(reserved) / float64(totalCapacity)
		liquidityScore = int(radarFullMark * (1.0 - usage))
	}

	// 2. Uptime Score (0-radarFullMark)
	uptimeScore := radarFullMark
	nodes := s.nodesService.GetNodes()
	if len(nodes) > 0 {
		peers, _ := nodes[0].Client.ListPeers()
		activeCount := 0
		for _, p := range peers {
			if p.Active {
				activeCount++
			}
		}
		if len(peers) > 0 {
			uptimeScore = int(radarFullMark * (float64(activeCount) / float64(len(peers))))
		}
	}

	// 3. Success Score (0-radarFullMark)
	// Base success score weighted by order outcomes
	successScore := (radarFullMark * 85) / 100 // 85% base
	orders, _ := s.openingStore.ListLsps1Orders(defaultListLimit, 0)
	if len(orders) > 0 {
		completed := 0
		failed := 0
		for _, o := range orders {
			if o.OrderState == string(lsps.Lsps1OrderStateCompleted) {
				completed++
			} else if o.OrderState == string(lsps.Lsps1OrderStateFailed) {
				failed++
			}
		}
		if completed+failed > 0 {
			successScore = int(radarFullMark * (float64(completed) / float64(completed+failed)))
		}
	}

	data := []map[string]interface{}{
		{"subject": "Liquidity", "A": liquidityScore, "fullMark": radarFullMark},
		{"subject": "Uptime", "A": uptimeScore, "fullMark": radarFullMark},
		{"subject": "Speed", "A": 120, "fullMark": radarFullMark}, // TODO: compute real speed
		{"subject": "Success", "A": successScore, "fullMark": radarFullMark},
		{"subject": "Fees", "A": 110, "fullMark": radarFullMark}, // TODO: compute fee score
		{"subject": "Compliance", "A": radarFullMark, "fullMark": radarFullMark},
	}
	return lsps.NewJsonRpcResponse(req.Id, data)
}

func (s *Handlers) handleGetAnalytics(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	// Fetch a generous amount of recent orders to compute stats
	orders, _ := s.openingStore.ListLsps1Orders(analyticsListLimit, 0)

	// 1. Order Volume by State & Core Stats
	volume := map[string]int{
		string(lsps.Lsps1OrderStateCreated):   0,
		string(lsps.Lsps1OrderStatePaid):      0,
		string(lsps.Lsps1OrderStateCompleted): 0,
		string(lsps.Lsps1OrderStateFailed):    0,
		string(lsps.Lsps1OrderStateExpired):   0,
	}

	var totalRevenue int64 = 0
	var completedCount int64 = 0

	// 2. History maps
	revenueMap := make(map[string]int64)
	volumeMap := make(map[string]int64)
	countMap := make(map[string]int)

	now := time.Now()
	for i := 0; i < analyticsHistoryDays; i++ {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		revenueMap[day] = 0
		volumeMap[day] = 0
		countMap[day] = 0
	}

	for _, o := range orders {
		volume[o.OrderState]++

		day := o.CreatedAt.Format("2006-01-02")

		// Map revenue and volume history (Completed or Paid)
		if o.OrderState == string(lsps.Lsps1OrderStateCompleted) || o.OrderState == string(lsps.Lsps1OrderStatePaid) {
			if _, ok := revenueMap[day]; ok {
				revenueMap[day] += o.OrderTotalLoki
				volumeMap[day] += o.LspBalanceLoki
			}
			if o.OrderState == string(lsps.Lsps1OrderStateCompleted) {
				totalRevenue += o.OrderTotalLoki
				completedCount++
			}
		}

		// Map total orders per day
		if _, ok := countMap[day]; ok {
			countMap[day]++
		}
	}

	// Calculate final stats
	successRate := 0.0
	totalOrders := len(orders)
	if totalOrders > 0 {
		// Completed / (Completed + Failed) as a simple business success rate
		relevant := volume[string(lsps.Lsps1OrderStateCompleted)] + volume[string(lsps.Lsps1OrderStateFailed)]
		if relevant > 0 {
			successRate = (float64(volume[string(lsps.Lsps1OrderStateCompleted)]) / float64(relevant)) * 100
		}
	}

	avgOrderSize := int64(0)
	if completedCount > 0 {
		avgOrderSize = totalRevenue / completedCount
	}

	// Active peers count (real from nodesService)
	activePeers := 0
	nodes := s.nodesService.GetNodes()
	if len(nodes) > 0 {
		peers, _ := nodes[0].Client.ListPeers()
		for _, p := range peers {
			if p.Active {
				activePeers++
			}
		}
	}

	// Format history for frontend
	history := make([]map[string]interface{}, 0, 14)
	for i := 13; i >= 0; i-- {
		t := now.AddDate(0, 0, -i)
		dayKey := t.Format("2006-01-02")
		history = append(history, map[string]interface{}{
			"date":    dayKey,
			"day":     t.Format("Mon"),
			"revenue": revenueMap[dayKey],
			"count":   countMap[dayKey],
			"volume":  revenueMap[dayKey], // Volume often equals revenue for this dashboard
		})
	}

	return lsps.NewJsonRpcResponse(req.Id, map[string]interface{}{
		"total_revenue":   totalRevenue,
		"success_rate":    successRate,
		"avg_order_size":  avgOrderSize,
		"active_peers":    activePeers,
		"revenue_history": history,
		"order_distribution": []map[string]interface{}{
			{"name": "CREATED", "value": volume[string(lsps.Lsps1OrderStateCreated)], "color": "#94a3b8"},
			{"name": "PAID", "value": volume[string(lsps.Lsps1OrderStatePaid)], "color": "#00baff"},
			{"name": "COMPLETED", "value": volume[string(lsps.Lsps1OrderStateCompleted)], "color": "#00fd87"},
			{"name": "FAILED", "value": volume[string(lsps.Lsps1OrderStateFailed)], "color": "#ff3b30"},
			{"name": "EXPIRED", "value": volume[string(lsps.Lsps1OrderStateExpired)], "color": "#4a5568"},
		},
	})
}

func (s *Handlers) handleGetSummary(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	// Fetch real data from stores/services
	nodes := s.nodesService.GetNodes()
	if len(nodes) == 0 {
		return lsps.NewJsonRpcErrorResponse(req.Id, -32000, "No nodes configured", nil)
	}
	mainNode := nodes[0]

	// Aggregate metrics
	totalCapacity, free, reserved := s.getLiquidityMetrics(context.Background())

	// Real Metrics for Summary
	var revenue24h int64 = 0
	var revenuePrev24h int64 = 0

	orderStates := map[string]int{
		string(lsps.Lsps1OrderStateCreated):   0,
		string(lsps.Lsps1OrderStatePaid):      0,
		string(lsps.Lsps1OrderStateCompleted): 0,
		string(lsps.Lsps1OrderStateFailed):    0,
		string(lsps.Lsps1OrderStateExpired):   0,
	}

	orders, _ := s.openingStore.ListLsps1Orders(summaryListLimit, 0)
	now := time.Now()
	last24h := now.Add(-24 * time.Hour)
	prev24h := now.Add(-48 * time.Hour)

	for _, o := range orders {
		if o.CreatedAt.After(last24h) {
			if o.OrderState == string(lsps.Lsps1OrderStateCompleted) || o.OrderState == string(lsps.Lsps1OrderStatePaid) {
				revenue24h += o.OrderTotalLoki
			}
		} else if o.CreatedAt.After(prev24h) {
			if o.OrderState == string(lsps.Lsps1OrderStateCompleted) || o.OrderState == string(lsps.Lsps1OrderStatePaid) {
				revenuePrev24h += o.OrderTotalLoki
			}
		}

		// Count current "active" states (from recent 1000 orders)
		if _, ok := orderStates[o.OrderState]; ok {
			orderStates[o.OrderState]++
		}
	}

	// Calculate Trends
	calcTrend := func(curr, prev int64) string {
		if prev == 0 {
			if curr > 0 {
				return "+100%"
			}
			return "0%"
		}
		diff := float64(curr - prev)
		percent := (diff / float64(prev)) * 100
		if percent >= 0 {
			return fmt.Sprintf("+%.1f%%", percent)
		}
		return fmt.Sprintf("%.1f%%", percent)
	}

	revenueTrend := calcTrend(revenue24h, revenuePrev24h)

	// Node health: a single GetInfo() call replaces the old separate
	// GetBlockHeight() + ListPeers() calls (the latter's only use was an
	// "active peer" ratio that was always 100% -- LND's ListPeers RPC only
	// ever returns already-connected peers).
	nodeStatus := "unreachable"
	var syncedToChain bool
	var syncHeight uint32
	var peersConnected, channelsActive, channelsPending, channelsInactive uint32

	info, err := mainNode.Client.GetInfo()
	if err != nil {
		log.Warn().Err(err).Msg("Dashboard: GetInfo failed for summary")
	} else {
		syncedToChain = info.SyncedToChain
		syncHeight = info.BlockHeight
		peersConnected = info.NumPeers
		channelsActive = info.NumActiveChannels
		channelsPending = info.NumPendingChannels
		channelsInactive = info.NumInactiveChannels
		if syncedToChain {
			nodeStatus = "online"
		} else {
			nodeStatus = "syncing"
		}
	}

	// Real per-channel outbound/inbound liquidity, summed over currently
	// active channels only (pending-open and temporarily-inactive channels
	// aren't usable capacity yet).
	var outboundLiquidity, inboundLiquidity uint64
	channels, err := mainNode.Client.ListChannels()
	if err != nil {
		log.Warn().Err(err).Msg("Dashboard: ListChannels failed for summary")
	} else {
		for _, c := range channels {
			if c.Active {
				outboundLiquidity += uint64(c.LocalBalance)
				inboundLiquidity += uint64(c.RemoteBalance)
			}
		}
	}

	// Create simplified summary
	summary := SummaryResponse{
		NodeName:          mainNode.NodeConfig.Load().Name,
		NodePubkey:        mainNode.NodeConfig.Load().NodePubkey,
		NodeHost:          mainNode.NodeConfig.Load().Host,
		Version:           build.GetTag(),
		Revenue24h:        uint64(revenue24h),
		LiquidityTotal:    totalCapacity,
		LiquidityFree:     free,
		LiquidityBlocked:  reserved,
		LiquidityOutbound: outboundLiquidity,
		LiquidityInbound:  inboundLiquidity,
		ActiveOrders:      orderStates,
		NodeStatus:        nodeStatus,
		SyncedToChain:     syncedToChain,
		SyncHeight:        syncHeight,
		PeersConnected:    peersConnected,
		ChannelsActive:    channelsActive,
		ChannelsPending:   channelsPending,
		ChannelsInactive:  channelsInactive,
		RevenueTrend:      revenueTrend,
	}

	return lsps.NewJsonRpcResponse(req.Id, summary)
}

func (s *Handlers) handleListOrders(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	// For LSP dashboard, we usually want recent orders across all nodes we manage
	orders, err := s.openingStore.ListLsps1Orders(defaultListLimit, 0)
	if err != nil {
		log.Error().Err(err).Msg("Dashboard: failed to list orders")
		return lsps.NewJsonRpcErrorResponse(req.Id, -32603, "Internal error", nil)
	}

	// Enrich with peer metadata
	nodes := s.nodesService.GetNodes()
	peerMap := make(map[string]*lightning.Peer)
	var activeClient lightning.Client
	if len(nodes) > 0 {
		activeClient = nodes[0].Client
		peers, _ := activeClient.ListPeers()
		for _, p := range peers {
			peerMap[p.Pubkey] = p
		}
	}

	for i := range orders {
		if p, ok := peerMap[orders[i].PeerId]; ok {
			orders[i].PeerAlias = p.Alias
			orders[i].PeerColor = p.Color
		} else if activeClient != nil {
			// Fallback: look up node directly from the graph for offline peers
			info, err := activeClient.GetNodeInfo(orders[i].PeerId)
			if err == nil && info != nil {
				orders[i].PeerAlias = info.Alias
				orders[i].PeerColor = info.Color

				// Cache to prevent duplicate graph lookups for the same offline peer
				peerMap[orders[i].PeerId] = &lightning.Peer{
					Pubkey: info.Pubkey,
					Alias:  info.Alias,
					Color:  info.Color,
				}
			}
		}
	}

	return lsps.NewJsonRpcResponse(req.Id, orders)
}

func (s *Handlers) handleListPeers(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	nodes := s.nodesService.GetNodes()
	var allPeers []interface{}

	// 1. Get order counts per peer
	orderCounts := make(map[string]int)
	orders, _ := s.openingStore.ListLsps1Orders(summaryListLimit, 0)
	for _, o := range orders {
		orderCounts[o.PeerId]++
	}

	for _, node := range nodes {
		peers, err := node.Client.ListPeers()
		if err != nil {
			log.Warn().Err(err).Msgf("Dashboard: failed to list peers for node %x", node.NodeId)
			continue
		}
		for _, p := range peers {
			// Enrich with order count
			count := orderCounts[p.Pubkey]

			// Map to consistent JSON structure
			enriched := map[string]interface{}{
				"pub_key":     p.Pubkey,
				"alias":       p.Alias,
				"color":       p.Color,
				"address":     p.Address,
				"inbound":     p.Inbound,
				"active":      p.Active,
				"in_loki":     p.LokiReceived, // Money received FROM peer (inbound for us)
				"out_loki":    p.LokiSent,     // Money sent TO peer (outbound for us)
				"order_count": count,
			}
			allPeers = append(allPeers, enriched)
		}
	}

	return lsps.NewJsonRpcResponse(req.Id, allPeers)
}

func (s *Handlers) handleListJitChannels(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	// JIT channels are represented by LSPS1 orders that have been successfully funded
	orders, err := s.openingStore.ListLsps1Orders(defaultListLimit, 0)
	if err != nil {
		return lsps.NewJsonRpcErrorResponse(req.Id, -32603, "Internal error", nil)
	}

	// 1. Fetch active channels from all nodes to check real-time status
	activeChannels := make(map[string]*lightning.Channel)
	nodes := s.nodesService.GetNodes()
	for _, node := range nodes {
		chans, err := node.Client.ListChannels()
		if err != nil {
			log.Warn().Err(err).Msgf("Dashboard: failed to list channels for node %x", node.NodeId)
			continue
		}
		for _, c := range chans {
			if c.ChannelPoint != nil {
				// Key by TXID:Index
				activeChannels[c.ChannelPoint.String()] = c
			}
		}
	}

	// 2. Correlate orders with active channels
	var enrichedChannels []interface{}
	for _, o := range orders {
		// Only show orders that have a funding TXID (attempted or successful openings)
		if o.FundingTxid == nil || *o.FundingTxid == "" {
			continue
		}

		// Construct the outpoint string to match ListChannels format (e.g. "txid:idx")
		outpointStr := fmt.Sprintf("%s:%d", *o.FundingTxid, 0) // Default to index 0
		if o.FundingOutpoint != nil {
			outpointStr = fmt.Sprintf("%s:%d", *o.FundingTxid, *o.FundingOutpoint)
		}

		activeCh, ok := activeChannels[outpointStr]

		status := "OFFLINE"
		scid := ""
		if ok {
			status = "ACTIVE"
			if activeCh.ConfirmedScid != nil {
				scid = activeCh.ConfirmedScid.ToString()
			} else if activeCh.AliasScid != nil {
				scid = activeCh.AliasScid.ToString()
			}
		}

		// Map to a frontend-friendly object
		enriched := map[string]interface{}{
			"order_id":   o.OrderId,
			"channel_id": scid, // Show real SCID if active
			"peer_id":    o.PeerId,
			"capacity":   o.LspBalanceLoki,
			"fee":        o.OrderTotalLoki,
			"status":     status,
			"created_at": o.CreatedAt,
			"txid":       *o.FundingTxid,
		}
		enrichedChannels = append(enrichedChannels, enriched)
	}

	return lsps.NewJsonRpcResponse(req.Id, enrichedChannels)
}

// configSecretFields are the JSON keys of config.NodeConfig fields that
// must never leave the server: the LSP's private key, client auth tokens,
// and the LND connection block (which carries the macaroon and TLS cert).
// See C-1 — the dashboard used to return the full NodeConfig, secrets
// included, to any authenticated browser client.
var configSecretFields = []string{"flspdPrivateKey", "tokens", "flnd"}

// stripConfigSecrets returns cfg re-marshaled with configSecretFields
// removed, safe to send to the dashboard UI.
func stripConfigSecrets(cfg *config.NodeConfig) (map[string]interface{}, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for _, field := range configSecretFields {
		delete(m, field)
	}
	return m, nil
}

func (s *Handlers) handleGetConfig(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	node, _, errResp := s.selectNode(req)
	if errResp != nil {
		return errResp
	}
	// The selected node's config, with secrets stripped (C-1).
	safeConfig, err := stripConfigSecrets(node.NodeConfig.Load())
	if err != nil {
		log.Error().Err(err).Msg("Dashboard: failed to prepare config for response")
		return lsps.NewJsonRpcErrorResponse(req.Id, -32603, "Internal error", nil)
	}
	relays := s.announceRelays
	if relays == nil {
		relays = []string{}
	}
	return lsps.NewJsonRpcResponse(req.Id, map[string]interface{}{
		"config":          safeConfig,
		"announce_relays": relays,
	})
}

func (s *Handlers) handleUpdateConfig(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	var params struct {
		Config config.NodeConfig `json:"config"`
	}
	target, targetIdx, errResp := s.selectNode(req)
	if errResp != nil {
		return errResp
	}

	if err := json.Unmarshal(req.Params, &params); err != nil {
		return lsps.NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params", err.Error())
	}

	// 1. Validate the new configuration before applying
	if err := params.Config.Validate(); err != nil {
		log.Error().Err(err).Msg("Dashboard: configuration validation failed")
		return lsps.NewJsonRpcErrorResponse(req.Id, -32602, "Validation failed", err.Error())
	}

	s.configMu.Lock()
	defer s.configMu.Unlock()
	nodes := s.nodesService.GetNodes()

	// The request body never carries the selected node's secrets (they're
	// stripped on the way out, C-1), so preserve them from its current
	// in-memory config rather than clobbering them with zero values.
	newConfig := params.Config
	current := target.NodeConfig.Load()
	newConfig.FlspdPrivateKey = current.FlspdPrivateKey
	newConfig.Tokens = current.Tokens
	newConfig.Lnd = current.Lnd

	// Announcing needs more than a valid announce block: relays and a
	// macaroon that lets the node key sign (T8-1/T8-2). The check reads the
	// preserved flnd block above, never anything from the request.
	if s.announceCheck != nil && newConfig.Announcing() {
		if err := s.announceCheck(&newConfig); err != nil {
			log.Error().Err(err).Msg("Dashboard: announce requirements not met")
			return lsps.NewJsonRpcErrorResponse(req.Id, -32602, "Validation failed", err.Error())
		}
	}

	// 2. Persist to file first (if configPath is set).
	// This ensures we don't update memory if persistence fails.
	if s.configPath != "" {
		// Read-modify-write the full node list (C-2): a naive
		// []*config.NodeConfig{&newConfig} single-element write here would
		// silently drop every other configured node from the file on a
		// multi-node deployment. Build the full list from the live
		// in-memory nodes, with only the selected node replaced.
		configs := make([]*config.NodeConfig, len(nodes))
		for idx, n := range nodes {
			configs[idx] = n.NodeConfig.Load()
		}
		configs[targetIdx] = &newConfig

		data, err := yaml.Marshal(configs)
		if err != nil {
			log.Error().Err(err).Msg("Failed to marshal config for persistence")
			return lsps.NewJsonRpcErrorResponse(req.Id, -32603, "Failed to marshal configuration", err.Error())
		}

		// Write a temporary file next to the config, then rename it over.
		tmpPath, err := writeTempConfig(s.configPath, data)
		if err != nil {
			log.Error().Err(err).Msg("Failed to write temporary config file")
			return lsps.NewJsonRpcErrorResponse(req.Id, -32603, "Failed to write configuration file", err.Error())
		}

		err = os.Rename(tmpPath, s.configPath)
		if err != nil {
			log.Error().Err(err).Msg("Failed to rename temporary config file")
			os.Remove(tmpPath) // clean up
			return lsps.NewJsonRpcErrorResponse(req.Id, -32603, "Failed to persist configuration", err.Error())
		}

		log.Info().Msgf("Persisted dashboard config changes to %s", s.configPath)
	}

	// 3. Update in memory only after successful persistence. Store()
	// atomically swaps in the new config rather than mutating the shared
	// struct in place, so concurrent readers on the HTLC hot path (the
	// interceptor, the settlement service) never see a torn read (C-3).
	target.NodeConfig.Store(&newConfig)

	// An announced value may have changed: let the publisher compare now
	// rather than at its next poll.
	if s.announcePublisher != nil {
		s.announcePublisher.Trigger()
	}

	return lsps.NewJsonRpcResponse(req.Id, map[string]string{"status": "updated"})
}

// writeTempConfig writes data to a new temporary file in configPath's
// directory, so the rename over configPath stays on one filesystem, and
// returns its path. Each call gets its own file.
func writeTempConfig(configPath string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(configPath), filepath.Base(configPath)+".*.tmp")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Chmod(0644); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (s *Handlers) handleGetOrderDetail(req *lsps.JsonRpcRequest) *lsps.JsonRpcResponse {
	var params struct {
		OrderId string `json:"order_id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.OrderId == "" {
		return lsps.NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params: order_id required", nil)
	}

	order, err := s.openingStore.GetLsps1Order(params.OrderId)
	if err != nil {
		log.Error().Err(err).Msgf("Dashboard: failed to get order %s", params.OrderId)
		return lsps.NewJsonRpcErrorResponse(req.Id, -32603, "Order not found", nil)
	}

	// Enrich with peer metadata
	nodes := s.nodesService.GetNodes()
	if len(nodes) > 0 {
		info, err := nodes[0].Client.GetNodeInfo(order.PeerId)
		if err == nil && info != nil {
			order.PeerAlias = info.Alias
			order.PeerColor = info.Color
		}
	}

	// Build a rich response with timeline events
	timeline := []map[string]interface{}{
		{"event": "ORDER_CREATED", "timestamp": order.CreatedAt.Format(time.RFC3339), "detail": "Order registered by peer"},
	}
	if order.PaymentState == string(lsps.Lsps1PaymentStatePaid) || order.OrderState == string(lsps.Lsps1OrderStateCompleted) {
		timeline = append(timeline, map[string]interface{}{
			"event":     "PAYMENT_RECEIVED",
			"timestamp": order.CreatedAt.Format(time.RFC3339), // Recorded at same time for now
			"detail":    "Invoice payment confirmed",
		})
	}

	if order.FundingTxid != nil && *order.FundingTxid != "" {
		timeline = append(timeline, map[string]interface{}{
			"event":     "CHANNEL_FUNDED",
			"timestamp": order.CreatedAt.Format(time.RFC3339), // Recorded at same time for now
			"detail":    "Funding TX: " + *order.FundingTxid,
		})
	}

	if order.OrderState == string(lsps.Lsps1OrderStateCompleted) {
		timeline = append(timeline, map[string]interface{}{
			"event":     "ORDER_COMPLETED",
			"timestamp": order.CreatedAt.Format(time.RFC3339), // Recorded at same time for now
			"detail":    "Channel opened and confirmed",
		})
	}

	result := map[string]interface{}{
		"order":    order,
		"timeline": timeline,
	}
	return lsps.NewJsonRpcResponse(req.Id, result)
}

func (s *Handlers) getLiquidityMetrics(ctx context.Context) (total, free, reserved uint64) {
	reserved, _ = s.openingStore.GetReservedLiquidity()

	nodes := s.nodesService.GetNodes()
	if len(nodes) > 0 {
		balance, err := nodes[0].Client.WalletBalance(ctx)
		if err == nil {
			free = uint64(balance.ConfirmedBalance)
			total = free + reserved
			return
		}
	}

	// Fallback to placeholder if node not available or error
	total = 500000000
	free = 500000000 - reserved
	return
}
