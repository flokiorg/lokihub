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
	"github.com/flokiorg/lokihub/utils"
)

type LiquidityManager struct {
	cfg        *ManagerConfig
	transport  transport.Transport
	eventQueue *events.EventQueue

	lsps0Client *lsps0.ClientHandler
	lsps1Client *lsps1.ClientHandler
	lsps2Client *lsps2.ClientHandler
	lsps5Client *lsps5.ClientHandler

	// Future service handlers
	// lsps2Service *lsps2.ServiceHandler

	mu sync.RWMutex

	listeners       map[string]chan events.Event
	unclaimedEvents map[string]events.Event

	// cache for dynamic nostr pubkeys (LSP Pubkey -> Nostr Pubkey)
	nostrPubkeys map[string]string
}

type PendingOrder struct {
	OrderID   string
	LSPPubkey string
	CreatedAt time.Time
}

// SettingsLSP represents a configured LSP in the settings
type SettingsLSP struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Pubkey      string `json:"pubkey"`
	Host        string `json:"host"`
	Active      bool   `json:"active"`
	IsCommunity bool   `json:"isCommunity"`
	NostrPubkey string `json:"nostrPubkey,omitempty"`
}

// JITJitChannelHints represents hints gathered from a JIT channel request
type JitChannelHints struct {
	SCID            string
	LSPNodeID       string
	CLTVExpiryDelta uint16
	FeeMloki        uint64
}

var ()

func NewLiquidityManager(cfg *ManagerConfig) (*LiquidityManager, error) {
	if cfg.LNClient == nil {
		return nil, fmt.Errorf("LNClient is required")
	}

	// Initialize basic components
	t := transport.NewLNDTransport(cfg.LNClient)
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

	return m, nil
}

func (m *LiquidityManager) Start(ctx context.Context) error {
	// subscribe to incoming messages
	msgs, errs, err := m.cfg.LNClient.SubscribeCustomMessages(ctx)
	if err != nil {
		return fmt.Errorf("failed to subscribe to custom messages: %w", err)
	}

	go m.processMessages(ctx, msgs, errs)
	go m.processInternalEvents(ctx)
	go m.StartInterceptor(ctx)

	// Start polling for pending orders
	go m.pollOrders(ctx)

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

				// Update DB with full details (state + amounts if available)
				if err := m.cfg.LSPManager.UpdateOrderState(order.OrderID, statusEvent.OrderState); err != nil {
					logger.Logger.Error().Err(err).Str("order_id", order.OrderID).Msg("Failed to update order state in DB")
				}

				// Publish event to internal queue (for any internal listeners)
				m.eventQueue.Enqueue(statusEvent)

				// Also publish to global event bus for Frontend updates (SSE)
				if m.cfg.EventPublisher != nil {
					m.cfg.EventPublisher.Publish(&globalevents.Event{
						Event: constants.LSPS5_EVENT_ORDER_STATE_CHANGED,
						Properties: map[string]interface{}{
							"order_id":   order.OrderID,
							"state":      statusEvent.OrderState,
							"lsp_pubkey": order.LSPPubkey,
							"timestamp":  time.Now().UTC().Format(time.RFC3339),
						},
					})
				}
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

// HandleOrderStateUpdate processes state updates from other sources (e.g. Webhook)
func (m *LiquidityManager) HandleOrderStateUpdate(orderID, state string) {
	logger.Logger.Info().Str("order_id", orderID).Str("state", state).Msg("Received external order state update")

	// Update DB
	if err := m.cfg.LSPManager.UpdateOrderState(orderID, state); err != nil {
		logger.Logger.Error().Err(err).Str("order_id", orderID).Msg("Failed to update order state from webhook")
		return
	}

	// We could also fetch the full order details if we needed more than just state,
	// but usually the notification payload (if rich) or just the state change is enough to notify frontend.
	// For now, let's just create a synthetic event or fetch fresh?
	// The webhook handler in http_service already publishes an event.
	// But `pollOrders` relies on DB state. Updating DB here is enough for `pollOrders` stop logic if it was stateful,
	// but pollOrders iterates over "non-terminal".
	// By updating to "COMPLETED" here, the next poll loop simply won't pick it up. Efficient!
}

func (m *LiquidityManager) processMessages(ctx context.Context, msgs <-chan lnclient.CustomMessage, errs <-chan error) {
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errs:
			if err != nil {
				// Log error (we might need a logger in config)
				logger.Logger.Error().Err(err).Msg("Error receiving custom message")
				continue
			}
		case msg := <-msgs:
			m.dispatchMessage(msg)
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
		nostrPub := ""
		if val, ok := m.nostrPubkeys[l.Pubkey]; ok {
			nostrPub = val
		}

		lspList = append(lspList, SettingsLSP{
			Name:        l.Name,
			Description: l.Description,
			Pubkey:      l.Pubkey,
			Host:        l.Host,
			Active:      l.IsActive,
			IsCommunity: l.IsCommunity,
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

// AddLSP adds a new LSP via the LSPManager
func (m *LiquidityManager) AddLSP(name, uri string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pubkey, host, err := utils.ParseLSPURI(uri)
	if err != nil {
		return err
	}

	// Determine if it should be active (if it's the first one?)
	// For now default to true as per old logic? Old logic: active = len(existing) == 0
	// We can check existing count
	all, err := m.cfg.LSPManager.ListLSPs()
	if err != nil {
		return err
	}
	active := len(all) == 0

	_, err = m.cfg.LSPManager.AddLSP(name, pubkey, host, active, false)
	return err
}

// RemoveLSP removes an LSP via LSPManager
func (m *LiquidityManager) RemoveLSP(pubkey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.LSPManager.DeleteCustomLSP(strings.ToLower(pubkey))
}

// SetActiveLSP enables an LSP
func (m *LiquidityManager) SetActiveLSP(pubkey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.LSPManager.ToggleLSP(strings.ToLower(pubkey), true)
}

// AddSelectedLSP marks an LSP as active
func (m *LiquidityManager) AddSelectedLSP(pubkey string) error {
	return m.SetActiveLSP(pubkey)
}

// RemoveSelectedLSP marks an LSP as inactive
func (m *LiquidityManager) RemoveSelectedLSP(pubkey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.LSPManager.ToggleLSP(strings.ToLower(pubkey), false)
}

// ConnectLSP attempts to connect to a specific LSP
func (m *LiquidityManager) ConnectLSP(ctx context.Context, pubkey string) error {
	lsps, err := m.GetLSPs()
	if err != nil {
		return err
	}

	var targetLSP *SettingsLSP
	for _, lsp := range lsps {
		if lsp.Pubkey == pubkey {
			targetLSP = &lsp
			break
		}
	}

	if targetLSP == nil {
		return fmt.Errorf("LSP %s not found in settings", pubkey)
	}

	host := targetLSP.Host
	if host == "" {
		return fmt.Errorf("LSP %s has no host configured", pubkey)
	}

	logger.Logger.Info().Str("lsp", pubkey).Str("host", host).Msg("Connecting to LSP due to notification")

	return m.cfg.LNClient.ConnectPeer(ctx, &lnclient.ConnectPeerRequest{
		Pubkey:  pubkey,
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
		if lsp.Active {
			selected = append(selected, lsp)
		}
	}
	return selected, nil
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

	inboundCapacityMsat := uint64(inboundCapacity) * 1000
	if inboundCapacityMsat >= amountMloki {
		// Sufficient liquidity
		return nil, nil
	}

	logger.Logger.Info().
		Uint64("amount_mloki", amountMloki).
		Uint64("inbound_mloki", inboundCapacityMsat).
		Msg("Insufficient inbound liquidity, attempting JIT channel buy")

	// 2. Get Active LSP
	lsps, err := m.GetSelectedLSPs()
	if err != nil || len(lsps) == 0 {
		return nil, fmt.Errorf("no active LSP to buy liquidity from")
	}
	activeLSP := lsps[0] // Use first active

	// 3. Helper to perform the Buy flow
	performBuy := func() (*JitChannelHints, error) {
		// A. Get Fee Params
		feeParamsMenu, err := m.GetLSPS2FeeParams(ctx, activeLSP.Pubkey)
		if err != nil {
			return nil, err
		}
		if len(feeParamsMenu) == 0 {
			return nil, fmt.Errorf("LSP returned no fee parameters")
		}
		// Select first param for now (simplest strategy)
		// We could implement "cheapest" later
		selectedParams := feeParamsMenu[0]

		// Calculate Fee logic (Standardized with Frontend)
		// Fee = Max(MinFee, Proportional)
		minFee := selectedParams.MinFeeMloki
		// Proportional is parts per million
		// amount * proportional / 1000000
		// We ceiling the result to be safe? JS used math.Ceil.
		// Integer math: (amount * prop + 999999) / 1000000
		proportionalFee := (amountMloki*uint64(selectedParams.Proportional) + 999999) / 1000000

		feeMloki := minFee
		if proportionalFee > feeMloki {
			feeMloki = proportionalFee
		}

		// B. Buy
		res, err := m.OpenJitChannel(ctx, activeLSP.Pubkey, amountMloki, selectedParams)
		if err != nil {
			return nil, err
		}

		return &JitChannelHints{
			SCID:            fmt.Sprintf("%d", res.InterceptSCID),
			LSPNodeID:       res.LSPNodeID,
			CLTVExpiryDelta: res.CLTVExpiryDelta,
			FeeMloki:        feeMloki,
		}, nil
	}

	// 4. Try Buy (with retry logic)
	hints, err := performBuy()
	if err != nil {
		// Check for specific error codes if possible
		// Since OpenJitChannel wraps error strings, we check substring or improve OpenJitChannel.
		// BLIP-52 Code 201: invalid_opening_fee_params
		// flspd Code 100: "Invalid promise or expired params" (maps to same retryable condition)
		if strings.Contains(err.Error(), "invalid_opening_fee_params") || strings.Contains(err.Error(), "LSP error 100") {
			logger.Logger.Warn().Msg("JIT Buy failed due to stale params, retrying once...")
			// Simple Retry
			return performBuy()
		}
		return nil, err
	}

	return hints, nil
}

// BuyLiquidity buys inbound liquidity from a specific LSP with robust error handling (retries on stale params).
func (m *LiquidityManager) BuyLiquidity(ctx context.Context, lspPubkey string, amountMloki uint64, manualParams *lsps2.OpeningFeeParams) (*JitChannelHints, error) {
	// Helper to perform the Buy flow
	performBuy := func(params *lsps2.OpeningFeeParams) (*JitChannelHints, error) {
		var selectedParams lsps2.OpeningFeeParams
		if params != nil {
			selectedParams = *params
		} else {
			// A. Get Fee Params
			feeParamsMenu, err := m.GetLSPS2FeeParams(ctx, lspPubkey)
			if err != nil {
				return nil, err
			}
			if len(feeParamsMenu) == 0 {
				return nil, fmt.Errorf("LSP returned no fee parameters")
			}
			// Select first param for now (simplest strategy)
			selectedParams = feeParamsMenu[0]
		}

		// B. Buy
		res, err := m.OpenJitChannel(ctx, lspPubkey, amountMloki, selectedParams)
		if err != nil {
			return nil, err
		}

		// Calculate Fee logic
		minFee := selectedParams.MinFeeMloki
		proportionalFee := (amountMloki*uint64(selectedParams.Proportional) + 999999) / 1000000
		feeMloki := minFee
		if proportionalFee > feeMloki {
			feeMloki = proportionalFee
		}

		// Success
		return &JitChannelHints{
			SCID:            fmt.Sprintf("%d", res.InterceptSCID),
			LSPNodeID:       res.LSPNodeID,
			CLTVExpiryDelta: res.CLTVExpiryDelta,
			FeeMloki:        feeMloki,
		}, nil
	}

	// If manual params provided, try once (no retry, as invoice logic depends on these specific params)
	if manualParams != nil {
		hints, err := performBuy(manualParams)
		if err != nil {
			return nil, err
		}
		return hints, nil
	}

	// Automatic flow with retry
	hints, err := performBuy(nil)
	if err != nil {
		// BLIP-52 Code 201: invalid_opening_fee_params
		// flspd Code 100: "Invalid promise or expired params" (maps to same retryable condition)
		if strings.Contains(err.Error(), "invalid_opening_fee_params") || strings.Contains(err.Error(), "LSP error 100") {
			logger.Logger.Warn().Msg("Manual Buy failed due to stale params, retrying once...")
			// Simple Retry will fetch fresh params
			return performBuy(nil)
		}
		return nil, err
	}

	return hints, nil
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

func (m *LiquidityManager) CreateLSPS1Order(ctx context.Context, pubkey string, orderParams lsps1.OrderParams, refundAddr *string) (*lsps1.OrderCreatedEvent, error) {
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
	reqChan, respond, err := m.cfg.LNClient.SubscribeChannelAcceptor(ctx)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to subscribe to channel acceptor")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case req, ok := <-reqChan:
			if !ok {
				// Stream closed
				logger.Logger.Info().Msg("Channel acceptor stream closed, retrying in 5s...")
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
					// Retry subscription
					reqChan, respond, err = m.cfg.LNClient.SubscribeChannelAcceptor(ctx)
					if err != nil {
						logger.Logger.Error().Err(err).Msg("Failed to resubscribe to channel acceptor")
						// Exponential backoff or just loop? 5s fixed for now.
					}
					continue
				}
			}

			// Check against whitelist
			m.mu.RLock()
			activeLSPs, err := m.getLSPsFromDB()
			m.mu.RUnlock()

			if err != nil {
				logger.Logger.Error().Err(err).Msg("Failed to get LSPs from DB")
				// Check if whitelisted for ZeroConf
				whitelisted := false
				requestPubkey := strings.ToLower(req.NodePubkey)
				for _, lsp := range activeLSPs {
					if lsp.Active && strings.ToLower(lsp.Pubkey) == requestPubkey {
						whitelisted = true
						break
					}
				}

				if whitelisted {
					logger.Logger.Info().Str("pubkey", req.NodePubkey).Msg("Accepting ZeroConf channel from trusted LSP")
					respond(req.ID, true, true)
				} else {
					// Standard accept (ZeroConf=false)
					logger.Logger.Info().Str("pubkey", req.NodePubkey).Msg("Standard accept for channel from untrusted peer")
					respond(req.ID, true, false)
				}
				continue
			}

			whitelisted := false
			requestPubkey := strings.ToLower(req.NodePubkey)
			for _, lsp := range activeLSPs {
				// Check if active and pubkey matches
				if lsp.Active && strings.ToLower(lsp.Pubkey) == requestPubkey {
					whitelisted = true
					break
				}
			}

			if whitelisted {
				logger.Logger.Info().Str("pubkey", req.NodePubkey).Msg("Accepting ZeroConf channel from trusted LSP")
				respond(req.ID, true, true)
			} else {
				logger.Logger.Info().Str("pubkey", req.NodePubkey).Msg("Standard accept for channel from untrusted peer")
				// Accept normally (LND default validation)
				respond(req.ID, true, false)
			}
		}
	}
}
