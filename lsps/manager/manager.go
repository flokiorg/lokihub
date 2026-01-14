package manager

import (
	"context"
	"fmt"
	"sync" // Added import

	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/events"
	"github.com/flokiorg/lokihub/lsps/lsps0"
	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/lsps5"
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

	// Future service handlers
	// lsps2Service *lsps2.ServiceHandler

	mu sync.RWMutex

	listeners       map[string]chan events.Event
	unclaimedEvents map[string]events.Event
}

// SettingsLSP represents a configured LSP in the settings
type SettingsLSP struct {
	Name   string `json:"name"`
	Pubkey string `json:"pubkey"`
	Host   string `json:"host"`
	Active bool   `json:"active"`
}

var (
	// Pubkey hex validation
	pubkeyRegex = regexp.MustCompile(`^[0-9a-fA-F]{66}$`)
)

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

	return nil
}

func (m *LiquidityManager) processMessages(ctx context.Context, msgs <-chan lnclient.CustomMessage, errs <-chan error) {
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errs:
			if err != nil {
				// Log error (we might need a logger in config)
				fmt.Printf("Error receiving custom message: %v\n", err)
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

func (m *LiquidityManager) AddLSP(name, uri string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Validate Name
	if strings.TrimSpace(name) == "" {
		return errors.New("name cannot be empty")
	}

	// Check duplicates in DB
	existing, err := m.getLSPsFromDB()
	if err != nil {
		return fmt.Errorf("failed to read LSPs: %w", err)
	}

	for _, lsp := range existing {
		if strings.EqualFold(lsp.Name, name) {
			return fmt.Errorf("LSP with name '%s' already exists", name)
		}
	}

	// 2. Validate URI
	// Format: pubkey@host:port
	parts := strings.Split(uri, "@")
	if len(parts) != 2 {
		return errors.New("invalid URI format: expected pubkey@host:port")
	}
	pubkey := parts[0]
	host := parts[1]

	if !pubkeyRegex.MatchString(pubkey) {
		return errors.New("invalid pubkey format: expected 33-byte hex")
	}
	if host == "" {
		return errors.New("host cannot be empty")
	}

	// Check for duplicate pubkey
	for _, lsp := range existing {
		if lsp.Pubkey == pubkey {
			return fmt.Errorf("LSP with pubkey '%s' already exists", pubkey)
		}
	}

	// 3. Save
	newLSP := SettingsLSP{
		Name:   name,
		Pubkey: pubkey,
		Host:   host,
		Active: len(existing) == 0, // Auto-activate if first
	}
	existing = append(existing, newLSP)

	return m.saveLSPsToDB(existing)
}

func (m *LiquidityManager) GetLSPs() ([]SettingsLSP, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.getLSPsFromDB()
}

func (m *LiquidityManager) RemoveLSP(pubkey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, err := m.getLSPsFromDB()
	if err != nil {
		return err
	}

	var updated []SettingsLSP
	for _, lsp := range existing {
		if lsp.Pubkey != pubkey {
			updated = append(updated, lsp)
		}
	}

	if len(updated) == len(existing) {
		return errors.New("LSP not found")
	}

	return m.saveLSPsToDB(updated)
}

func (m *LiquidityManager) SetActiveLSP(pubkey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, err := m.getLSPsFromDB()
	if err != nil {
		return err
	}

	found := false
	for i := range existing {
		if existing[i].Pubkey == pubkey {
			existing[i].Active = true
			found = true
		} else {
			existing[i].Active = false // Enforce single active LSP for now?
			// User request says "select multiple... or add it self"
			// "only a name field is added to repsent the the lsp and we gonna use as label intead of display the URI"
			// If multi-select is allowed, we shouldn't disable others.
			// BUT user request says "Settings > Services > LSPs user could select multiple".
			// Let's assume multi-active is allowed.
			// RE-READING: "user could select multiple from community added sps or add it self"
			// Usually this means adding to a "My LSPs" list.
			// Active status usually implies "I want to use this one".
			// Mult-active LSPs is complex for "Receive" flow (which one to ask?).
			// Let's stick to single active for JIT/Receive for simplicity unless specified.
			// Wait, user said "select multiple...".
			// Let's assume we toggle the specific one requested.
		}
	}

	if !found {
		return errors.New("LSP not found")
	}

	// If sticking to single active:
	// The above loop disables others.

	return m.saveLSPsToDB(existing)
}

// GetSelectedLSPs returns all LSPs marked as active (selected)
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

// AddSelectedLSP marks an LSP as selected/active (supports multiple active LSPs)
func (m *LiquidityManager) AddSelectedLSP(pubkey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, err := m.getLSPsFromDB()
	if err != nil {
		return err
	}

	found := false
	for i := range existing {
		if existing[i].Pubkey == pubkey {
			existing[i].Active = true
			found = true
			break
		}
	}

	if !found {
		return errors.New("LSP not found")
	}

	return m.saveLSPsToDB(existing)
}

// RemoveSelectedLSP marks an LSP as not selected/inactive
func (m *LiquidityManager) RemoveSelectedLSP(pubkey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, err := m.getLSPsFromDB()
	if err != nil {
		return err
	}

	found := false
	for i := range existing {
		if existing[i].Pubkey == pubkey {
			existing[i].Active = false
			found = true
			break
		}
	}

	if !found {
		return errors.New("LSP not found")
	}

	return m.saveLSPsToDB(existing)
}

// Internal DB helpers
const dbKeyLSPs = "lsps_settings_list"

func (m *LiquidityManager) getLSPsFromDB() ([]SettingsLSP, error) {
	if m.cfg.KVStore == nil {
		return nil, nil // Should error? Or just empty list matching "no persistence"
	}
	data, err := m.cfg.KVStore.Read(dbKeyLSPs)
	if err != nil {
		return nil, nil // Assume not found / empty
	}
	if len(data) == 0 {
		return []SettingsLSP{}, nil
	}

	var list []SettingsLSP
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list, nil
}

func (m *LiquidityManager) saveLSPsToDB(list []SettingsLSP) error {
	if m.cfg.KVStore == nil {
		return errors.New("no persistence available")
	}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return m.cfg.KVStore.Write(dbKeyLSPs, data)
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
		return nil, fmt.Errorf("LSP returned error: %s", e.Error)
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
		return nil, fmt.Errorf("LSP returned error: %s", e.Error)
	default:
		return nil, fmt.Errorf("unexpected event type: %s", event.EventType())
	}
}
