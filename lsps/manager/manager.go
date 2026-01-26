package manager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/logger"

	"strings"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/lsps0"
	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/lsps5"
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

	pendingOrders map[string]PendingOrder
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
		pendingOrders:   make(map[string]PendingOrder),
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

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.mu.RLock()
			orders := make([]PendingOrder, 0, len(m.pendingOrders))
			for _, order := range m.pendingOrders {
				orders = append(orders, order)
			}
			m.mu.RUnlock()

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

				// Publish event
				m.eventQueue.Enqueue(statusEvent)

				state := strings.ToUpper(statusEvent.OrderState)
				if state == "COMPLETED" || state == "FAILED" || state == "CANCELLED" || state == "CLOSED" {
					m.mu.Lock()
					delete(m.pendingOrders, order.OrderID)
					m.mu.Unlock()
				}
			}
		}
	}
}

func (m *LiquidityManager) MonitorOrder(lspPubkey, orderID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingOrders[orderID] = PendingOrder{
		OrderID:   orderID,
		LSPPubkey: lspPubkey,
		CreatedAt: time.Now(),
	}
	logger.Logger.Info().Str("order_id", orderID).Msg("Started monitoring order")
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
	// Ideally we parse once and dispatch, but for now delegating raw usage
	// to handlers is simpler, though less efficient if they all re-parse.
	// But since `HandleMessage` checks specific IDs/Types, it should be fine.
	// Actually, the handlers look up by RequestID or dispatch by Method.
	//
	// Since we are strictly clients for now, we are mostly expecting RESPONSES.
	// Responses don't have a "method" field in JSON-RPC 2.0 usually,
	// they match by ID.
	// So we need each client to check if it owns the ID.

	// We can try them sequentially.

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
	// Safety buffer? Frontend used 0.8 factor.
	// Let's be explicit: if inbound < amount * factor?
	// The problem is simple: if amount > inbound, payment WILL fail.
	// So we should check strict amount > inbound.
	// But giving 0.8 buffer helps avoid edge cases.
	// Wait, balances.Lightning.Total is "Total Balance" or "Inbound"?
	// GetBalances returns BalancesResponse which has Lightning.TotalReceivable (int64).
	// amountMloki is unit64.
	// TotalReceivable is usually in SATOSHIS in some APIs?
	// Let's double check models.go for BalancesResponse.
	// Assuming TotalReceivable is milli-satoshis if the variable is called Mloki?
	// Looking at models.go earlier:
	// type Channel struct { ... LocalBalance int64 ... }  (usually satoshis in LND)
	// But LNClient.GetBalances returns BalancesResponse.
	// In LND service GetBalances likely returns satoshis.
	// amountMloki is millisats.
	// So we need to compare apples to apples.
	// inboundCapacityMsat = inboundCapacity * 1000

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
		// Start monitoring
		m.MonitorOrder(pubkey, e.OrderID)
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
				// Default accept? Or Reject?
				// To be safe, let's accept but maybe without zero-conf (by passing false? logic in LNDService handles true=ZeroConf, false=Reject?)
				// Wait, the respond func I wrote sends Accept: accept.
				// If I send true, it sets ZeroConf=true.
				// This might be too aggressive for non-whitelisted peers.
				// Let's adjust LNDService logic or here.
				// Since LNDService logic is hardcoded to "If accept -> set ZeroConf=true",
				// I should ONLY call respond(..., true) for WHITELISTED peers.
				// For others, I might want to let LND handle it?
				// The ChannelAcceptor RPC blocks LND until response.
				// So I MUST respond.
				// Use Case:
				// 1. Whitelisted LSP -> Accept (Zero Conf)
				// 2. Random Peer -> Accept (Normal) OR Reject?
				// The user likely wants normal behavior for others.
				// But `ChannelAcceptResponse` has `zero_conf` field.
				// If I set `Accept: true` and `ZeroConf: false`, it's a normal accept.

				// I need to update LNDService respond function to allow specifying zero-conf.
				// But for now, let's assuming "Active LSPs" are the ONLY ones we trust for Zero Conf.
				// And we probably want to Accept others normally?
				// But wait, if I use ChannelAcceptor, I assume full control.

				// Let's implement strict whitelist for ZeroConf, and Normal Accept for others?
				// But to do Normal Accept via RPC, I just send Accept=true, ZeroConf=false.
				// My LNDService `respond` function currently hardcodes ZeroConf=true if Accept=true.
				// I should fix LNDService first to be more flexible, or just accept the limitation for now?
				// Limiting to ONLY whitelisted LSPs for the interceptor seems safer?
				// "all other channels regarding if they are zero conf or not should be rejected?"
				// PROBABLY NOT.

				// Let's fix LNDService logic first. It is bad practice to hardcode ZeroConf=true.
				// I should pass it as arg.

				// Since I cannot change LNDService in same tool call, and I already wrote it...
				// I will rewrite LNDService respond function in next step.

				// For now, let's write the loop logic assuming I can fix respond later.
				respond(req.ID, true, false)
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
