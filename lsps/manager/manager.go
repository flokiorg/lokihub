package manager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/constants"
	globalevents "github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"

	"strings"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/lsps0"
	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/lsps5"
	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/flokiorg/lokihub/lsps/transport"
)

type LiquidityManager struct {
	cfg        *ManagerConfig
	transport  transport.Transport
	eventQueue *events.EventQueue

	lsps0Client *lsps0.ClientHandler
	lsps1Client *lsps1.ClientHandler
	lsps2Client *lsps2.ClientHandler
	lsps5Client *lsps5.ClientHandler

	connectionManager *ConnectionManager

	// Future service handlers
	// lsps2Service *lsps2.ServiceHandler

	mu sync.RWMutex

	listeners       map[string]chan events.Event
	unclaimedEvents map[string]events.Event

	// cache for dynamic nostr pubkeys (LSP Pubkey -> Nostr Pubkey)
	nostrPubkeys map[string]string

	// liveChecker runs NIP-LSP's live check before each purchase.
	liveChecker LiveChecker
}

type PendingOrder struct {
	OrderID   string
	LSPPubkey string
	CreatedAt time.Time
}

// SettingsLSP represents a configured LSP in the settings
type SettingsLSP struct {
	// NodeKey is the announcement's x-only key; Pubkey the node id LSPS uses.
	NodeKey     string `json:"nodeKey"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Website     string `json:"website"`
	Pubkey      string `json:"pubkey"`
	Host        string `json:"host"`
	// Trusted is the user's choice (LP-12); Status the announcement's status.
	Trusted     bool   `json:"trusted"`
	Status      string `json:"status"`
	Usable      bool   `json:"usable"`
	NostrPubkey string `json:"nostrPubkey,omitempty"`
}

// JITJitChannelHints represents hints gathered from a JIT channel request
type JitChannelHints struct {
	SCID      string
	LSPNodeID string
	// ClientTrustsLSP mirrors the buy response's client_trusts_lsp flag so the
	// per-buy trust posture reaches the API/UI (LOKIHUB-0028).
	ClientTrustsLSP bool
	CLTVExpiryDelta uint16
	FeeMloki        uint64
}

var ()

func NewLiquidityManager(cfg *ManagerConfig) (*LiquidityManager, error) {
	if cfg.LNClient == nil {
		return nil, fmt.Errorf("LNClient is required")
	}

	// Initialize basic components
	t := transport.NewFLNDTransport(cfg.LNClient)
	eq := events.NewEventQueue(100) // Buffer size 100

	m := &LiquidityManager{
		cfg:             cfg,
		transport:       t,
		eventQueue:      eq,
		listeners:       make(map[string]chan events.Event),
		unclaimedEvents: make(map[string]events.Event),
		nostrPubkeys:    make(map[string]string),
	}

	// Initialize Clients
	m.lsps0Client = lsps0.NewClientHandler(t, eq)
	m.lsps1Client = lsps1.NewClientHandler(t, eq)
	m.lsps2Client = lsps2.NewClientHandler(t, eq)
	m.lsps5Client = lsps5.NewClientHandler(t, eq)

	m.connectionManager = NewConnectionManager(cfg)

	return m, nil
}

func (m *LiquidityManager) Start(ctx context.Context) error {
	go m.processMessages(ctx)
	go m.processInternalEvents(ctx)
	go m.StartInterceptor(ctx)

	// Start Connection Manager
	m.connectionManager.Start(ctx)

	// Start polling for pending orders
	go m.pollOrders(ctx)

	// Warmup LSP cache (fetch Nostr pubkeys)
	go m.warmupLSPCache(ctx)

	return nil
}

func (m *LiquidityManager) pollOrders(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// Initial load of orders if needed (already handled by DB query inside loop which is fine)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Fetch pending orders from DB
			orders, err := m.cfg.LSPManager.ListPendingOrders()
			if err != nil {
				logger.Logger.Error().Err(err).Msg("Failed to list pending orders")
				continue
			}

			for _, order := range orders {
				statusEvent, err := m.GetLSPS1Order(ctx, order.LSPPubkey, order.OrderID)
				if err != nil {
					logger.Logger.Warn().Err(err).Str("order_id", order.OrderID).Msg("Failed to poll order status")
					continue
				}

				logger.Logger.Info().
					Str("order_id", order.OrderID).
					Str("state", statusEvent.OrderState).
					Msg("Polled order status")

				m.eventQueue.Enqueue(statusEvent)
				fundingOutpoint := ""
				if statusEvent.Channel != nil {
					fundingOutpoint = statusEvent.Channel.FundingOutpoint
				}
				m.HandleOrderStateUpdate(ctx, order.OrderID, statusEvent.OrderState, order.LSPPubkey, fundingOutpoint)
			}
		}
	}
}

func (m *LiquidityManager) warmupLSPCache(ctx context.Context) {
	// Wait a bit for connection manager to establish connections
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
	}

	lsps, err := m.GetSelectedLSPs()
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to get selected LSPs for warmup")
		return
	}

	for _, l := range lsps {
		// Only refresh if Nostr pubkey is missing
		if l.NostrPubkey == "" {
			logger.Logger.Info().Str("lsp", l.Pubkey).Msg("Warming up LSP Nostr Pubkey...")
			if _, err := m.RefreshLSPInfo(ctx, l.Pubkey); err != nil {
				logger.Logger.Warn().Err(err).Str("lsp", l.Pubkey).Msg("Failed to refresh LSP info during warmup")
			}
		}
	}
}

func (m *LiquidityManager) MonitorOrder(lspPubkey, orderID string, invoice string, feeTotal, orderTotal, lspBalance, clientBalance uint64) {
	// Save to DB
	order := &persist.LSPS1Order{
		OrderID:        orderID,
		LSPPubkey:      lspPubkey,
		State:          "CREATED",
		PaymentInvoice: invoice,
		FeeTotal:       feeTotal,
		OrderTotal:     orderTotal,
		LSPBalance:     lspBalance,
		ClientBalance:  clientBalance,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := m.cfg.LSPManager.CreateOrder(order); err != nil {
		logger.Logger.Error().Err(err).Str("order_id", orderID).Msg("Failed to persist new order")
	} else {
		logger.Logger.Info().Str("order_id", orderID).Msg("Persisted new order for monitoring")
	}
}

// orderStatePriority returns the priority of an order state. Higher priority states
// must never be overwritten by lower priority ones (e.g. PAID must not regress to CREATED
// when the poller receives a spec-mapped CREATED from flspd for a PAID order).
func orderStatePriority(state string) int {
	switch state {
	case "CREATED":
		return 0
	case "PAID":
		return 1
	case "COMPLETED", "FAILED", "EXPIRED", "CANCELLED", "CLOSED":
		return 2
	default:
		return 0
	}
}

// isKnownLSPS1OrderState is the whitelist of order_state values the Hub acts on.
// An LSP-reported state outside this set is ignored rather than stored
// (LOKIHUB-0026): an unknown string would otherwise be mapped by
// orderStatePriority to 0 (= CREATED) and churn the poller, and an unknown
// string treated as terminal could permanently lock the order.
func isKnownLSPS1OrderState(state string) bool {
	switch state {
	case "CREATED", "PAID", "COMPLETED", "FAILED", "EXPIRED", "CANCELLED", "CLOSED":
		return true
	default:
		return false
	}
}

// resolveLSPNodeID maps an LSP identifier to its 66-hex Lightning node id. A
// node id is returned unchanged; an x-only key (e.g. the LSP's Nostr
// notification pubkey, which the Nostr order-state path carries) is resolved
// against the catalogue to the owning LSP's node id. An unresolvable value is
// returned unchanged so the ownership guard in UpdateOrderStateIfPriority still
// rejects it — this maps the identity, it never weakens the guard
// (LOKIHUB-0026).
func (m *LiquidityManager) resolveLSPNodeID(pubkey string) string {
	if pubkey == "" {
		return pubkey
	}
	lsps, err := m.GetLSPs()
	if err != nil {
		return pubkey
	}
	for _, lsp := range lsps {
		if strings.EqualFold(lsp.Pubkey, pubkey) {
			return lsp.Pubkey
		}
	}
	for _, lsp := range lsps {
		if lsp.NostrPubkey != "" && strings.EqualFold(lsp.NostrPubkey, pubkey) {
			return lsp.Pubkey
		}
	}
	return pubkey
}

// verifyOrderChannel reports whether the node actually holds the channel an LSP
// claims to have opened for orderID — a channel to the order's LSP whose
// capacity is at least the quoted lsp_balance and, when the LSP reported a
// funding outpoint, that exact channel. This is the "show facts it knows
// itself" check (NIP-LSP §Security Considerations) that stops a
// malicious/compromised LSP from driving an order to COMPLETED after taking the
// fee without opening — or under-opening — the channel (LOKIHUB-0026). When the
// node can't be consulted, it returns false: an unverifiable COMPLETED is not
// trusted.
func (m *LiquidityManager) verifyOrderChannel(ctx context.Context, orderID, lspPubkey, fundingOutpoint string) bool {
	if m.cfg.LNClient == nil {
		return false
	}
	order, err := m.cfg.LSPManager.GetOrder(orderID)
	if err != nil {
		logger.Logger.Warn().Err(err).Str("order_id", orderID).Msg("Cannot reconcile COMPLETED order: order not found")
		return false
	}
	channels, err := m.cfg.LNClient.ListChannels(ctx)
	if err != nil {
		logger.Logger.Warn().Err(err).Str("order_id", orderID).Msg("Cannot reconcile COMPLETED order: ListChannels failed")
		return false
	}
	wantOutpoint := strings.ToLower(strings.TrimSpace(fundingOutpoint))
	for _, ch := range channels {
		if !strings.EqualFold(ch.RemotePubkey, lspPubkey) {
			continue
		}
		if wantOutpoint != "" {
			got := strings.ToLower(fmt.Sprintf("%s:%d", ch.FundingTxId, ch.FundingTxVout))
			if got != wantOutpoint {
				continue
			}
		}
		// Channel balances are in mloki; the quoted lsp_balance is in loki.
		capacityMloki := ch.LocalBalance + ch.RemoteBalance
		if capacityMloki >= 0 && uint64(capacityMloki) >= order.LSPBalance*1000 {
			return true
		}
		logger.Logger.Warn().
			Str("order_id", orderID).
			Int64("channel_capacity_mloki", capacityMloki).
			Uint64("quoted_lsp_balance_loki", order.LSPBalance).
			Msg("Channel for order found but its capacity is below the quoted lsp_balance")
	}
	return false
}

// minPendingQuoteForLSP returns the smallest quoted lsp_balance (loki) across
// the Hub's still-pending LSPS1 orders with the given LSP, and whether any such
// order exists. Used by the channel acceptor to refuse zero-conf for a channel
// smaller than the smallest inbound liquidity that LSP still owes us.
func (m *LiquidityManager) minPendingQuoteForLSP(lspPubkey string) (uint64, bool) {
	if m.cfg.LSPManager == nil {
		return 0, false
	}
	orders, err := m.cfg.LSPManager.ListPendingOrders()
	if err != nil {
		logger.Logger.Warn().Err(err).Msg("Failed to list pending orders for channel-accept capacity check")
		return 0, false
	}
	var min uint64
	found := false
	for _, o := range orders {
		if o.LSPBalance == 0 || !strings.EqualFold(o.LSPPubkey, lspPubkey) {
			continue
		}
		if !found || o.LSPBalance < min {
			min = o.LSPBalance
			found = true
		}
	}
	return min, found
}

// HandleOrderStateUpdate persists an order state change and publishes the SSE event to the frontend.
// It is the single authoritative path for all state change sources (Nostr, HTTP webhook, polling).
// The read and write are atomic inside a transaction — a lower-priority state can never overwrite a
// higher-priority one even under concurrent callers (e.g. PAID → CREATED is silently skipped).
func (m *LiquidityManager) HandleOrderStateUpdate(ctx context.Context, orderID, state, lspPubkey, fundingOutpoint string) {
	// A Nostr-transported notification is keyed by the LSP's x-only Nostr
	// pubkey, not its 66-hex node id; map it to the node id so the ownership
	// guard (which compares against order.LSPPubkey, a node id) can match. An
	// unresolvable value passes through unchanged and still fails the guard —
	// the guard is never weakened (LOKIHUB-0026).
	lspPubkey = m.resolveLSPNodeID(lspPubkey)

	// Act only on recognised order states (LOKIHUB-0026).
	if !isKnownLSPS1OrderState(state) {
		logger.Logger.Warn().
			Str("order_id", orderID).
			Str("state", state).
			Msg("Ignoring LSPS1 order update with unknown state")
		return
	}

	// Never trust a bare COMPLETED: verify the LSP actually opened the channel
	// it claims, against facts the node knows itself (LOKIHUB-0026). Computed
	// before the priority transaction; the ownership guard inside
	// UpdateOrderStateIfPriority still runs first for a forged notification.
	completedVerified := true
	if state == "COMPLETED" {
		completedVerified = m.verifyOrderChannel(ctx, orderID, lspPubkey, fundingOutpoint)
		if !completedVerified {
			logger.Logger.Warn().
				Str("order_id", orderID).
				Str("lsp", lspPubkey).
				Str("funding_outpoint", fundingOutpoint).
				Msg("LSP reported order COMPLETED but no matching channel found on the node; not marking completed")
		}
	}

	applied, err := m.cfg.LSPManager.UpdateOrderStateIfPriority(orderID, state, lspPubkey, func(current string) bool {
		if orderStatePriority(current) > orderStatePriority(state) {
			logger.Logger.Debug().
				Str("order_id", orderID).
				Str("current", current).
				Str("new", state).
				Msg("Skipping order state downgrade")
			return false
		}
		if state == "COMPLETED" && !completedVerified {
			return false
		}
		return true
	})
	if err != nil {
		logger.Logger.Error().Err(err).Str("order_id", orderID).Msg("Failed to persist order state")
		return
	}
	if !applied {
		return
	}

	logger.Logger.Info().Str("order_id", orderID).Str("state", state).Msg("Order state update")

	if m.cfg.EventPublisher != nil {
		m.cfg.EventPublisher.Publish(&globalevents.Event{
			Event: constants.LSPS5_EVENT_ORDER_STATE_CHANGED,
			Properties: map[string]interface{}{
				"order_id":   orderID,
				"state":      state,
				"lsp_pubkey": lspPubkey,
				"timestamp":  time.Now().UTC().Format(time.RFC3339),
			},
		})
	}
}

// streamRetryDelay is the backoff between stream resubscription attempts, shared
// by processMessages and StartInterceptor.
const streamRetryDelay = 5 * time.Second

func (m *LiquidityManager) processMessages(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		msgs, errs, err := m.cfg.LNClient.SubscribeCustomMessages(ctx)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to subscribe to custom messages, retrying")
			select {
			case <-ctx.Done():
				return
			case <-time.After(streamRetryDelay):
				continue
			}
		}
		logger.Logger.Info().Msg("Custom messages stream established")
		if !m.consumeMessageStream(ctx, msgs, errs) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(streamRetryDelay):
		}
	}
}

// consumeMessageStream reads from a live custom-message stream until it closes or
// errors. Returns true when the caller should resubscribe, false when the context
// is done.
func (m *LiquidityManager) consumeMessageStream(
	ctx context.Context,
	msgs <-chan lnclient.CustomMessage,
	errs <-chan error,
) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case err, ok := <-errs:
			if !ok || err != nil {
				if err != nil {
					logger.Logger.Error().Err(err).Msg("Custom messages stream error")
				}
				m.drainMessages(msgs)
				return true
			}
		case msg, ok := <-msgs:
			if !ok {
				logger.Logger.Info().Msg("Custom messages stream closed")
				return true
			}
			m.dispatchMessage(msg)
		}
	}
}

// drainMessages dispatches any messages already buffered in a closing stream so
// that in-flight LSPS request-response pairs are not silently dropped.
func (m *LiquidityManager) drainMessages(msgs <-chan lnclient.CustomMessage) {
	for {
		select {
		case msg, ok := <-msgs:
			if !ok {
				return
			}
			m.dispatchMessage(msg)
		default:
			return
		}
	}
}

func (m *LiquidityManager) dispatchMessage(msg lnclient.CustomMessage) {
	// Dispatch based on message logic or try all handlers?
	// LSPS1, LSPS2, LSPS5 share the same message type (37913 - LSPS0)
	// They distinguish by JSON-RPC method inside the payload.

	if msg.Type != lsps0.LSPS_MESSAGE_TYPE_ID {
		return
	}

	// We try to let each client handle it.
	// Since we are strictly clients for now, we are mostly expecting RESPONSES
	// which match by ID.

	// LSPS0
	if err := m.lsps0Client.HandleMessage(msg.PeerPubkey, msg.Data); err == nil {
		return
	}

	// LSPS1
	if err := m.lsps1Client.HandleMessage(msg.PeerPubkey, msg.Data); err == nil {
		return
	}

	// LSPS2
	if err := m.lsps2Client.HandleMessage(msg.PeerPubkey, msg.Data); err == nil {
		return
	}

	// LSPS5
	if err := m.lsps5Client.HandleMessage(msg.PeerPubkey, msg.Data); err == nil {
		return
	}

	// If none claimed it, it might be a notification (request from LSP)
	// For notifications (like lsps5.payment_incoming), they have a "method".
	// We probably need a unified dispatcher if this gets complex.
	// For now, this chain of responsibility is acceptable as a starting point.
}

func (m *LiquidityManager) LSPS0Client() *lsps0.ClientHandler {
	return m.lsps0Client
}

func (m *LiquidityManager) LSPS1Client() *lsps1.ClientHandler {
	return m.lsps1Client
}

func (m *LiquidityManager) LSPS2Client() *lsps2.ClientHandler {
	return m.lsps2Client
}

func (m *LiquidityManager) LSPS5Client() *lsps5.ClientHandler {
	return m.lsps5Client
}

func (m *LiquidityManager) EventQueue() *events.EventQueue {
	return m.eventQueue
}

// LSP Management API

// getLSPsFromDB fetches LSPs from the new DB manager and converts to internal SettingsLSP
func (m *LiquidityManager) getLSPsFromDB() ([]SettingsLSP, error) {
	dbLSPs, err := m.cfg.LSPManager.ListLSPs()
	if err != nil {
		return nil, err
	}

	var lspList []SettingsLSP
	for _, l := range dbLSPs {
		nostrPub := l.NostrPubkey
		if val, ok := m.nostrPubkeys[l.Pubkey]; ok && val != "" {
			nostrPub = val
		}

		lspList = append(lspList, SettingsLSP{
			NodeKey:     l.NodeKey,
			Name:        l.Name,
			Description: l.Description,
			Website:     l.Website,
			Pubkey:      l.Pubkey,
			Host:        l.Host,
			Trusted:     l.Trusted,
			Status:      l.Status,
			Usable:      l.Usable,
			NostrPubkey: nostrPub,
		})
	}
	return lspList, nil
}

// GetNostrPubkey returns the cached Nostr Pubkey for a given LSP
func (m *LiquidityManager) GetNostrPubkey(pubkey string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nostrPubkeys[pubkey]
}

// RefreshLSPInfo fetches LSPS0 info for a specific LSP and updates the nostr pubkey cache
func (m *LiquidityManager) RefreshLSPInfo(ctx context.Context, pubkey string) (string, error) {
	lsps0Info, err := m.lsps0Client.GetInfo(ctx, pubkey)
	if err != nil {
		return "", err
	}

	nostrPubkey := lsps0Info.NotificationNostrPubkey
	if nostrPubkey != "" {
		m.mu.Lock()
		m.nostrPubkeys[pubkey] = nostrPubkey
		m.mu.Unlock()

		// Persist to DB
		if err := m.cfg.LSPManager.UpdateLSPNostrPubkey(pubkey, nostrPubkey); err != nil {
			logger.Logger.Warn().Err(err).Str("lsp", pubkey).Msg("Failed to persist LSP Nostr Pubkey to DB")
		}

		logger.Logger.Debug().
			Str("lsp", pubkey).
			Str("nostr_pubkey", nostrPubkey).
			Msg("Cached LSP Nostr Pubkey")
	}

	return nostrPubkey, nil
}

// EnsureWebhookRegistered registers webhook for an LSP (called on-demand during order creation)
func (m *LiquidityManager) EnsureWebhookRegistered(ctx context.Context, lspPubkey string) error {
	if m.cfg.GetWebhookConfig == nil {
		return nil // Webhook config not available
	}

	webhookURL, transport := m.cfg.GetWebhookConfig()
	if webhookURL == "" || transport == "" {
		return nil // No webhook configured
	}

	// Get LSP's Nostr pubkey (try cache first, then fetch)
	m.mu.RLock()
	nostrPubkey := m.nostrPubkeys[lspPubkey]
	m.mu.RUnlock()

	if nostrPubkey == "" {
		// Fetch from LSPS0
		var err error
		nostrPubkey, err = m.RefreshLSPInfo(ctx, lspPubkey)
		if err != nil {
			logger.Logger.Warn().Err(err).Str("lsp", lspPubkey).Msg("Failed to get LSP info for webhook registration")
			// Continue anyway - webhook might still work
		}
	}

	if nostrPubkey == "" && transport == "nostr" {
		logger.Logger.Warn().Str("lsp", lspPubkey).Msg("Skipping Nostr webhook: LSP Nostr pubkey unknown")
		return nil
	}

	_, err := m.lsps5Client.SetWebhook(ctx, lspPubkey, constants.APP_IDENTIFIER, webhookURL, transport)
	if err != nil {
		logger.Logger.Warn().Err(err).Str("lsp", lspPubkey).Msg("Failed to register webhook")
		return err
	}

	logger.Logger.Info().Str("lsp", lspPubkey).Str("transport", transport).Msg("Registered webhook with LSP")
	return nil
}

// SetTrusted records the user's trust in an LSP (WP-26); the list changes only
// through the trust API.
func (m *LiquidityManager) SetTrusted(key string, trusted bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.LSPManager.SetTrusted(key, trusted)
}

// ConnectLSP attempts to connect to a specific LSP
func (m *LiquidityManager) ConnectLSP(ctx context.Context, pubkey string) error {
	lsps, err := m.GetLSPs()
	if err != nil {
		return err
	}

	var targetLSP *SettingsLSP
	for _, lsp := range lsps {
		if lsp.Pubkey == pubkey || lsp.NostrPubkey == pubkey {
			targetLSP = &lsp
			break
		}
	}

	if targetLSP == nil || !targetLSP.Trusted {
		// No connection is made to an LSP the user has not trusted (NIP-LSP
		// §Privacy Considerations).
		return fmt.Errorf("LSP %s is not a trusted LSP", pubkey)
	}
	if !connectable(targetLSP.Usable, targetLSP.Status) {
		// Never through an older announcement's address.
		return fmt.Errorf("LSP %s has no usable open announcement", pubkey)
	}

	host := targetLSP.Host
	if host == "" {
		return fmt.Errorf("LSP %s has no host configured", pubkey)
	}

	logger.Logger.Info().Str("lsp", pubkey).Str("host", host).Msg("Connecting to LSP due to notification")

	return m.cfg.LNClient.ConnectPeer(ctx, &lnclient.ConnectPeerRequest{
		Pubkey:  targetLSP.Pubkey,
		Address: host,
	})
}

// GetLSPs returns all LSPs
func (m *LiquidityManager) GetLSPs() ([]SettingsLSP, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.getLSPsFromDB()
}

func (m *LiquidityManager) GetSelectedLSPs() ([]SettingsLSP, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	all, err := m.getLSPsFromDB()
	if err != nil {
		return nil, err
	}

	var selected []SettingsLSP
	for _, lsp := range all {
		if lsp.Trusted {
			selected = append(selected, lsp)
		}
	}
	return selected, nil
}

// GetSellingLSPs are the trusted LSPs offering something new now: a usable
// announcement with status open and a resolved node id (NIP-LSP §Client
// Behavior: nothing new through a paused one, a closed one is not shown).
func (m *LiquidityManager) GetSellingLSPs() ([]SettingsLSP, error) {
	selected, err := m.GetSelectedLSPs()
	if err != nil {
		return nil, err
	}
	var out []SettingsLSP
	for _, l := range selected {
		if l.Usable && l.Status == "open" && l.Pubkey != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// Event Dispatching
func (m *LiquidityManager) processInternalEvents(ctx context.Context) {
	for {
		event, err := m.eventQueue.NextEvent(ctx)
		if err != nil {
			return
		}

		var requestID string
		switch e := event.(type) {
		case *lsps2.OpeningParametersReadyEvent:
			requestID = e.RequestID
		case *lsps2.GetInfoFailedEvent:
			requestID = e.RequestID
		case *lsps2.InvoiceParametersReadyEvent:
			requestID = e.RequestID
		case *lsps2.BuyRequestFailedEvent:
			requestID = e.RequestID
		// LSPS1 Events
		case *lsps1.SupportedOptionsReadyEvent:
			requestID = e.RequestID
		case *lsps1.SupportedOptionsFailedEvent:
			requestID = e.RequestID
		case *lsps1.OrderCreatedEvent:
			requestID = e.RequestID
		case *lsps1.OrderRequestFailedEvent:
			requestID = e.RequestID
		case *lsps1.OrderStatusEvent:
			requestID = e.RequestID
		default:
			// Unhandled event type
			continue
		}

		m.mu.Lock()
		if ch, ok := m.listeners[requestID]; ok {
			ch <- event
			delete(m.listeners, requestID)
		} else {
			m.unclaimedEvents[requestID] = event
			// Optional: Clean up old unclaimed events?
		}
		m.mu.Unlock()
	}
}

func (m *LiquidityManager) waitForEvent(ctx context.Context, requestID string) (events.Event, error) {
	m.mu.Lock()
	if event, ok := m.unclaimedEvents[requestID]; ok {
		delete(m.unclaimedEvents, requestID)
		m.mu.Unlock()
		return event, nil
	}

	ch := make(chan events.Event, 1)
	m.listeners[requestID] = ch
	m.mu.Unlock()

	select {
	case event := <-ch:
		return event, nil
	case <-ctx.Done():
		m.mu.Lock()
		delete(m.listeners, requestID)
		m.mu.Unlock()
		return nil, ctx.Err()
	}
}

// EnsureInboundLiquidity checks if the node has sufficient inbound liquidity for the amount.
// If NOT, it automatically performs a JIT channel opening with the active LSP.
// This handles the "JIT Smart Logic" on the backend.
// ListLSPS1Orders returns all tracked LSPS1 orders
func (m *LiquidityManager) ListLSPS1Orders() ([]persist.LSPS1Order, error) {
	return m.cfg.LSPManager.ListAllOrders()
}

func (m *LiquidityManager) EnsureInboundLiquidity(ctx context.Context, amountMloki uint64) (*JitChannelHints, error) {

	// 1. Check current liquidity
	balances, err := m.cfg.LNClient.GetBalances(ctx, false)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to check balances for JIT")
		// if we can't check balance, we probably shouldn't blindly try to buy liquidity?
		// Or should we fail safe? Let's return error.
		return nil, err
	}

	inboundCapacity := balances.Lightning.TotalReceivable
	// We ensure we compare apples to apples (mloki).

	inboundCapacityMloki := uint64(inboundCapacity) * 1000 //nolint:gosec // LN-node-reported receivable capacity is always non-negative
	if inboundCapacityMloki >= amountMloki {
		// Sufficient liquidity
		return nil, nil
	}

	logger.Logger.Info().
		Uint64("amount_mloki", amountMloki).
		Uint64("inbound_mloki", inboundCapacityMloki).
		Msg("Insufficient inbound liquidity, attempting JIT channel buy")

	// 2. Get Active LSP: the first trusted LSP selling now whose stored live
	// check did not find it unusable, and whose live check, run now, passes
	// (NIP-LSP §Client Behavior: before each purchase).
	lsps, err := m.GetSellingLSPs()
	if err != nil || len(lsps) == 0 {
		return nil, fmt.Errorf("no trusted LSP to buy liquidity from: choose one in Settings")
	}
	var activeLSP *SettingsLSP
	var lastRefusal error
	for i := range lsps {
		if row, err := m.cfg.LSPManager.GetLSP(lsps[i].Pubkey); err != nil || !storedCheckUsable(row) {
			continue
		}
		if err := m.CheckPurchase(ctx, lsps[i].Pubkey, true); err != nil {
			lastRefusal = err
			logger.Logger.Warn().Err(err).Str("lsp", lsps[i].Pubkey).Msg("Not buying JIT liquidity from this LSP")
			continue
		}
		activeLSP = &lsps[i]
		break
	}
	if activeLSP == nil {
		if lastRefusal != nil {
			return nil, lastRefusal
		}
		return nil, fmt.Errorf("%w: every trusted LSP's last live check found it unusable", ErrLSPNotUsable)
	}

	return m.buyCheckedJIT(ctx, activeLSP.Pubkey, amountMloki)
}

// isStaleParams is an LSPS2 buy refused for expired or unknown fee params:
// BLIP-52 code 201 invalid_opening_fee_params, or flspd's code 100.
func isStaleParams(err error) bool {
	return strings.Contains(err.Error(), "invalid_opening_fee_params") || strings.Contains(err.Error(), "LSP error 100")
}

// buyCheckedJIT is an automatic JIT purchase: it buys the first row of a menu
// fetched now only once that menu compares with the announcement as not worse
// for the buyer (NIP-LSP §Client Behavior, Live check), since no one can be
// asked again. A buy refused for stale params is tried once more, on a menu
// fetched and compared again.
func (m *LiquidityManager) buyCheckedJIT(ctx context.Context, lspPubkey string, amountMloki uint64) (*JitChannelHints, error) {
	hints, err := m.buyCheckedJITOnce(ctx, lspPubkey, amountMloki)
	if err != nil && isStaleParams(err) {
		logger.Logger.Warn().Msg("JIT buy failed due to stale params, retrying once...")
		return m.buyCheckedJITOnce(ctx, lspPubkey, amountMloki)
	}
	return hints, err
}

func (m *LiquidityManager) buyCheckedJITOnce(ctx context.Context, lspPubkey string, amountMloki uint64) (*JitChannelHints, error) {
	row, err := m.contactable(lspPubkey)
	if err != nil {
		return nil, err
	}
	menu, err := m.GetLSPS2FeeParams(ctx, lspPubkey)
	if err != nil {
		return nil, err
	}
	if len(menu) == 0 {
		return nil, fmt.Errorf("LSP returned no fee parameters")
	}
	terms, err := compareFeeMenu(row, menu)
	if err != nil {
		return nil, err
	}
	if terms.Worse {
		return nil, fmt.Errorf("%w: %w", ErrLSPNotUsable, &TermsWorseError{Terms: terms})
	}
	return m.openJIT(ctx, lspPubkey, amountMloki, menu[0])
}

// openJIT buys a JIT channel under params after checking its fee against the
// payment.
func (m *LiquidityManager) openJIT(ctx context.Context, lspPubkey string, amountMloki uint64, params lsps2.OpeningFeeParams) (*JitChannelHints, error) {
	feeMloki, err := jitFee(amountMloki, params)
	if err != nil {
		return nil, err
	}
	res, err := m.OpenJitChannel(ctx, lspPubkey, amountMloki, params)
	if err != nil {
		return nil, err
	}
	// The invoice's final route-hint hop MUST point at the counterparty we
	// actually bought from — the known peer lspPubkey — NOT the lsp_node_id the
	// LSP echoes back in its buy response (a non-standard field the LSP fully
	// controls). An LSP that returns a different id would otherwise make the Hub
	// mint invoices whose last hop is the wrong node, so payments can't route to
	// the intercept SCID (self-DoS). We only surface a mismatch (LOKIHUB-0027).
	if res.LSPNodeID != "" && !strings.EqualFold(res.LSPNodeID, lspPubkey) {
		logger.Logger.Warn().
			Str("lsp", lspPubkey).
			Str("response_lsp_node_id", res.LSPNodeID).
			Msg("LSPS2 buy response echoed a lsp_node_id different from the peer bought from; ignoring it and using the known peer pubkey")
	}
	return &JitChannelHints{
		SCID:            fmt.Sprintf("%d", res.InterceptSCID),
		LSPNodeID:       lspPubkey,
		ClientTrustsLSP: res.ClientTrustsLSP,
		CLTVExpiryDelta: res.CLTVExpiryDelta,
		FeeMloki:        feeMloki,
	}, nil
}

// BuyLiquidity buys a JIT channel from a specific LSP. Without manualParams it
// is an automatic purchase (buyCheckedJIT). With them it buys exactly that fee
// row, the one the user was shown: refused with a *TermsWorseError while a
// value worse than announced is not in accepted at its live value.
func (m *LiquidityManager) BuyLiquidity(ctx context.Context, lspPubkey string, amountMloki uint64, manualParams *lsps2.OpeningFeeParams, accepted []AcceptedTerm) (*JitChannelHints, error) {
	row, _, err := m.checkPurchase(ctx, lspPubkey, manualParams == nil)
	if err != nil {
		return nil, err
	}
	if manualParams == nil {
		return m.buyCheckedJIT(ctx, lspPubkey, amountMloki)
	}
	terms, err := compareFeeRow(row, *manualParams)
	if err != nil {
		return nil, err
	}
	if !terms.acceptedBy(accepted) {
		return nil, &TermsWorseError{Terms: terms}
	}
	// One try: the invoice depends on these exact params.
	return m.openJIT(ctx, lspPubkey, amountMloki, *manualParams)
}

// Synchronous LSPS2 Wrappers

func (m *LiquidityManager) GetLSPS2FeeParams(ctx context.Context, pubkey string) ([]lsps2.OpeningFeeParams, error) {
	reqID, err := m.lsps2Client.RequestOpeningParams(ctx, pubkey, nil)
	if err != nil {
		return nil, err
	}

	event, err := m.waitForEvent(ctx, reqID)
	if err != nil {
		return nil, err
	}

	switch e := event.(type) {
	case *lsps2.OpeningParametersReadyEvent:
		return e.OpeningFeeParamsMenu, nil
	case *lsps2.GetInfoFailedEvent:
		return nil, fmt.Errorf("LSP returned error (code %d): %s", e.ErrorCode, e.Error)
	default:
		return nil, fmt.Errorf("unexpected event type: %s", event.EventType())
	}
}

func (m *LiquidityManager) OpenJitChannel(ctx context.Context, pubkey string, paymentSizeMloki uint64, feeParams lsps2.OpeningFeeParams) (*lsps2.InvoiceParametersReadyEvent, error) {
	reqID, err := m.lsps2Client.SelectOpeningParams(ctx, pubkey, &paymentSizeMloki, feeParams)
	if err != nil {
		return nil, err
	}

	event, err := m.waitForEvent(ctx, reqID)
	if err != nil {
		return nil, err
	}

	switch e := event.(type) {
	case *lsps2.InvoiceParametersReadyEvent:
		return e, nil
	case *lsps2.BuyRequestFailedEvent:
		// We embed the Code in the error string or use a custom error type?
		// For simplicity in this function, we embed it so EnsureInboundLiquidity can parse it via string (as error types need more boilerplate)
		// Or return a formatted error: "LSP error 201: invalid_..."
		if e.ErrorCode == 201 {
			return nil, fmt.Errorf("LSP error 201: invalid_opening_fee_params: %s", e.Error)
		}
		if e.ErrorCode == 100 {
			return nil, fmt.Errorf("LSP error 100: invalid_opening_fee_params: %s", e.Error)
		}
		return nil, fmt.Errorf("LSP returned error (code %d): %s", e.ErrorCode, e.Error)
	default:
		return nil, fmt.Errorf("unexpected event type: %s", event.EventType())
	}
}

// Synchronous LSPS1 Wrappers

func (m *LiquidityManager) GetLSPS1Info(ctx context.Context, pubkey string) (lsps1.Options, error) {
	reqID, err := m.lsps1Client.RequestSupportedOptions(ctx, pubkey)
	if err != nil {
		return lsps1.Options{}, err
	}

	event, err := m.waitForEvent(ctx, reqID)
	if err != nil {
		return lsps1.Options{}, err
	}

	switch e := event.(type) {
	case *lsps1.SupportedOptionsReadyEvent:
		// Assuming we just return the single options object
		// But SupportedOptions in event is type Options? Check events.go/client.go
		// In client.go: SupportedOptions is type Options (struct).
		return e.SupportedOptions, nil
	case *lsps1.SupportedOptionsFailedEvent:
		return lsps1.Options{}, fmt.Errorf("LSP returned error: %s", e.Error)
	default:
		return lsps1.Options{}, fmt.Errorf("unexpected event type: %s", event.EventType())
	}
}

func (m *LiquidityManager) GetLSPS1InfoList(ctx context.Context, pubkey string) ([]lsps1.Options, error) {
	reqID, err := m.lsps1Client.RequestSupportedOptions(ctx, pubkey)
	if err != nil {
		return nil, err
	}

	event, err := m.waitForEvent(ctx, reqID)
	if err != nil {
		return nil, err
	}

	switch e := event.(type) {
	case *lsps1.SupportedOptionsReadyEvent:
		// Return slice containing the single options object
		return []lsps1.Options{e.SupportedOptions}, nil
	case *lsps1.SupportedOptionsFailedEvent:
		return nil, fmt.Errorf("LSP returned error: %s", e.Error)
	default:
		return nil, fmt.Errorf("unexpected event type: %s", event.EventType())
	}
}

// CreateLSPS1Order orders a channel. The live check runs first; an LSPS1
// value it finds worse than announced refuses the order with a
// *TermsWorseError until accepted holds it at its live value.
func (m *LiquidityManager) CreateLSPS1Order(ctx context.Context, pubkey string, orderParams lsps1.OrderParams, refundAddr *string, accepted []AcceptedTerm) (*lsps1.OrderCreatedEvent, error) {
	_, live, err := m.checkPurchase(ctx, pubkey, false)
	if err != nil {
		return nil, err
	}
	if live != nil {
		if terms := termsOf(lsps1Only(live.Fields)); !terms.acceptedBy(accepted) {
			return nil, &TermsWorseError{Terms: terms}
		}
	}
	reqID, err := m.lsps1Client.CreateOrder(ctx, pubkey, orderParams, refundAddr)
	if err != nil {
		return nil, err
	}

	event, err := m.waitForEvent(ctx, reqID)
	if err != nil {
		return nil, err
	}

	switch e := event.(type) {
	case *lsps1.OrderCreatedEvent:
		invoice := ""
		fee := uint64(0)
		total := uint64(0)
		if e.Payment.Bolt11 != nil {
			invoice = e.Payment.Bolt11.Invoice
			fee = e.Payment.Bolt11.FeeTotalLoki
			total = e.Payment.Bolt11.OrderTotalLoki
		}
		// Start monitoring with persistence
		m.MonitorOrder(pubkey, e.OrderID, invoice, fee, total, orderParams.LspBalanceLoki, orderParams.ClientBalanceLoki)
		return e, nil
	case *lsps1.OrderRequestFailedEvent:
		return nil, fmt.Errorf("LSP returned error: %s", e.Error)
	default:
		return nil, fmt.Errorf("unexpected event type: %s", event.EventType())
	}
}

func (m *LiquidityManager) GetLSPS1Order(ctx context.Context, pubkey, orderID string) (*lsps1.OrderStatusEvent, error) {
	reqID, err := m.lsps1Client.CheckOrderStatus(ctx, pubkey, orderID)
	if err != nil {
		return nil, err
	}

	event, err := m.waitForEvent(ctx, reqID)
	if err != nil {
		return nil, err
	}

	switch e := event.(type) {
	case *lsps1.OrderStatusEvent:
		return e, nil
	case *lsps1.OrderRequestFailedEvent:
		return nil, fmt.Errorf("LSP returned error: %s", e.Error)
	default:
		return nil, fmt.Errorf("unexpected event type: %s", event.EventType())
	}
}

// Interceptor Logic

func (m *LiquidityManager) StartInterceptor(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		reqChan, respond, err := m.cfg.LNClient.SubscribeChannelAcceptor(ctx)
		if err != nil {
			logger.Logger.Error().Err(err).Msg("Failed to subscribe to channel acceptor, retrying")
			select {
			case <-ctx.Done():
				return
			case <-time.After(streamRetryDelay):
				continue
			}
		}
		logger.Logger.Info().Msg("Channel acceptor stream established")
		if !m.runChannelAcceptor(ctx, reqChan, respond) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(streamRetryDelay):
		}
	}
}

// runChannelAcceptor processes channel-accept requests until the stream closes.
// Returns true when the caller should resubscribe, false when the context is done.
func (m *LiquidityManager) runChannelAcceptor(
	ctx context.Context,
	reqChan <-chan lnclient.ChannelAcceptRequest,
	respond func(id string, accept bool, zeroConf bool) error,
) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case req, ok := <-reqChan:
			if !ok {
				logger.Logger.Info().Msg("Channel acceptor stream closed, resubscribing")
				return true
			}
			m.handleChannelAcceptRequest(req, respond)
		}
	}
}

// handleChannelAcceptRequest enforces the LSP zero-conf whitelist and responds
// to an incoming channel-open request.
func (m *LiquidityManager) handleChannelAcceptRequest(
	req lnclient.ChannelAcceptRequest,
	respond func(id string, accept bool, zeroConf bool) error,
) {
	m.mu.RLock()
	activeLSPs, err := m.getLSPsFromDB()
	m.mu.RUnlock()

	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to get LSPs from DB")
		logger.Logger.Warn().Str("pubkey", req.NodePubkey).
			Msg("ZeroConf channel acceptance degraded to standard accept due to DB error")
	}

	requestPubkey := strings.ToLower(req.NodePubkey)
	whitelisted := false
	for _, lsp := range activeLSPs {
		// Zero-conf only while the trusted LSP's newest announcement is usable
		// and not closed (NIP-LSP §Client Behavior).
		if lsp.Trusted && connectable(lsp.Usable, lsp.Status) && strings.ToLower(lsp.Pubkey) == requestPubkey {
			whitelisted = true
			break
		}
	}

	if whitelisted {
		// Even from a trusted LSP, don't grant instant zero-conf trust to a
		// channel smaller than the smallest inbound liquidity that LSP still
		// owes us (a pending order's quoted lsp_balance). An under-capacity
		// open falls back to a standard (confirmed) accept, so the COMPLETED
		// reconciliation still gates the order (LOKIHUB-0026).
		if minQuoted, ok := m.minPendingQuoteForLSP(requestPubkey); ok && req.Capacity < minQuoted {
			logger.Logger.Warn().
				Str("pubkey", req.NodePubkey).
				Uint64("capacity", req.Capacity).
				Uint64("min_quoted_lsp_balance", minQuoted).
				Msg("Trusted LSP channel capacity below the smallest quoted lsp_balance; declining zero-conf, accepting as standard")
			if err := respond(req.ID, true, false); err != nil {
				logger.Logger.Error().Err(err).Str("pubkey", req.NodePubkey).
					Msg("Failed to send standard channel accept response")
			}
			return
		}
		logger.Logger.Info().Str("pubkey", req.NodePubkey).Msg("Accepting ZeroConf channel from trusted LSP")
		if err := respond(req.ID, true, true); err != nil {
			logger.Logger.Error().Err(err).Str("pubkey", req.NodePubkey).
				Msg("Failed to send ZeroConf channel accept response")
		}
	} else {
		logger.Logger.Info().Str("pubkey", req.NodePubkey).Msg("Standard accept for channel from untrusted peer")
		if err := respond(req.ID, true, false); err != nil {
			logger.Logger.Error().Err(err).Str("pubkey", req.NodePubkey).
				Msg("Failed to send standard channel accept response")
		}
	}
}
