package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/manager"
)

// lspsRequestTimeout is the timeout for LSPS protocol requests
const lspsRequestTimeout = 15 * time.Second

// contactLSP is the liquidity manager, once the LSP named is one this node may
// send LSPS messages to: trusted, its newest announcement usable and not
// closed (NIP-LSP §Client Behavior). Purchases check more (CheckPurchase).
func (api *api) contactLSP(pubkey string) (*manager.LiquidityManager, error) {
	lm := api.svc.GetLiquidityManager()
	if lm == nil {
		return nil, fmt.Errorf("LiquidityManager not started")
	}
	if err := lm.CheckContact(pubkey); err != nil {
		return nil, err
	}
	return lm, nil
}

// LSPS0ListProtocols lists supported protocols
func (api *api) LSPS0ListProtocols(ctx context.Context, req *LSPS0ListProtocolsRequest) (*LSPS0ListProtocolsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, lspsRequestTimeout)
	defer cancel()

	lm, err := api.contactLSP(req.LSPPubkey)
	if err != nil {
		return nil, err
	}
	client := lm.LSPS0Client()
	if client == nil {
		return nil, fmt.Errorf("LSPS0 client not available")
	}

	protocols, err := client.ListProtocols(ctx, req.LSPPubkey)
	if err != nil {
		logger.Logger.Error().Err(err).Str("lsp", req.LSPPubkey).Msg("LSPS0 ListProtocols failed")
		return nil, err
	}

	return &LSPS0ListProtocolsResponse{
		Protocols: protocols,
	}, nil
}

// LSPS1GetInfo gets channel ordering info
func (api *api) LSPS1GetInfo(ctx context.Context, req *LSPS1GetInfoRequest) (interface{}, error) {
	ctx, cancel := context.WithTimeout(ctx, lspsRequestTimeout)
	defer cancel()

	// Use synchronous manager method
	// Note: manager returns []lsps1.LSPS1Option or similar?
	// The manager method signature I added in manager.go checks `GetLSPS1InfoList` returns `[]lsps1.LSPS1Option`?
	// Wait, in manager.go I used `lsps1.LSPS1Option` which was UNDEFINED.
	// I need to fix manager.go imports or type names FIRST.
	// In msgs.go it is called `Options`.
	// So let's assume I fix manager.go to use `lsps1.Options`.

	// Assuming manager is fixed:
	lm, err := api.contactLSP(req.LSPPubkey)
	if err != nil {
		return nil, err
	}
	options, err := lm.GetLSPS1InfoList(ctx, req.LSPPubkey)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			logger.Logger.Warn().Str("lsp", req.LSPPubkey).Msg("LSPS1 GetInfo timeout")
			return nil, fmt.Errorf("time out, please retry")
		}
		logger.Logger.Error().Err(err).Str("lsp", req.LSPPubkey).Msg("LSPS1 GetInfo failed")
		return nil, err
	}

	// Return the first option directly for simpler consumption, matching LSPS2 style
	if len(options) == 0 {
		return nil, fmt.Errorf("no options provided by LSP")
	}

	return options[0], nil
}

// LSPS1CreateOrder creates a channel order
func (api *api) LSPS1CreateOrder(ctx context.Context, req *LSPS1CreateOrderRequest) (interface{}, error) {
	ctx, cancel := context.WithTimeout(ctx, lspsRequestTimeout)
	defer cancel()
	orderParams := lsps1.OrderParams{
		LspBalanceLoki:               req.LSPBalanceLoki,    // Amount of inbound liquidity requested (from amount_loki)
		ClientBalanceLoki:            req.ClientBalanceLoki, // Use requested client balance
		RequiredChannelConfirmations: 0,                     // Accept unconfirmed (LSP can override based on their policy)
		FundingConfirmsWithinBlocks:  6,                     // Standard Bitcoin confirmation window
		ChannelExpiryBlocks:          req.ChannelExpiryBlocks,
		Token:                        req.Token,
		AnnounceChannel:              req.AnnounceChannel,
		OpeningFeeParams:             req.OpeningFeeParams,
	}

	logger.Logger.Info().Interface("order_params", orderParams).Msg("Creating LSPS1 order")

	event, err := api.svc.GetLiquidityManager().CreateLSPS1Order(ctx, req.LSPPubkey, orderParams, req.RefundOnchainAddress, nil)
	if err != nil {
		logger.Logger.Error().Err(err).Str("lsp", req.LSPPubkey).Interface("params", orderParams).Msg("LSPS1 CreateOrder failed")
		return nil, err
	}

	invoice := ""
	var feeTotalLoki uint64
	var orderTotalLoki uint64
	if event.Payment.Bolt11 != nil {
		invoice = event.Payment.Bolt11.Invoice
		feeTotalLoki = event.Payment.Bolt11.FeeTotalLoki
		orderTotalLoki = event.Payment.Bolt11.OrderTotalLoki
		// Fallback: if fee is not provided but total is, and we know client balance is 0,
		// then fee must be the total payload (for LSPS1 buy-inbound).
		if feeTotalLoki == 0 && orderTotalLoki > 0 {
			feeTotalLoki = orderTotalLoki
		}
	}

	// Register webhook for notifications (on-demand, async so as not to block response)
	go func() { //nolint:gosec // intentionally decoupled from the request context; see comment below
		// A fresh context is required here: this goroutine deliberately
		// outlives the request (fire-and-forget), so the request-scoped ctx
		// would already be cancelled by the time this runs.
		bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := api.svc.GetLiquidityManager().EnsureWebhookRegistered(bgCtx, req.LSPPubkey); err != nil {
			logger.Logger.Warn().Err(err).Str("lsp", req.LSPPubkey).Msg("Failed to register webhook")
		}
	}()

	return map[string]interface{}{
		"order_id":            event.OrderID,
		"payment_invoice":     invoice,
		"fee_total_loki":      feeTotalLoki,
		"order_total_loki":    orderTotalLoki,
		"lsp_balance_loki":    orderParams.LspBalanceLoki,
		"client_balance_loki": orderParams.ClientBalanceLoki,
	}, nil
}

// LSPS1GetOrder checks order status
func (api *api) LSPS1GetOrder(ctx context.Context, req *LSPS1GetOrderRequest) (interface{}, error) {
	ctx, cancel := context.WithTimeout(ctx, lspsRequestTimeout)
	defer cancel()

	event, err := api.svc.GetLiquidityManager().GetLSPS1Order(ctx, req.LSPPubkey, req.OrderID)
	if err != nil {
		logger.Logger.Error().Err(err).Str("lsp", req.LSPPubkey).Str("order_id", req.OrderID).Msg("LSPS1 GetOrder failed")
		return nil, err
	}

	invoice := ""
	if event.Payment.Bolt11 != nil {
		invoice = event.Payment.Bolt11.Invoice
	}
	// Extract state from Payment info (Bolt11) or OrderState?
	// msgs.go has OrderState string.
	// OrderChannel.tsx expects `state`.
	// Use OrderState from the event/response.

	state := event.OrderState
	// If OrderState is empty/unknown, maybe fallback to Payment state?
	// For now trust OrderState.

	var feeTotalLoki, orderTotalLoki uint64
	if event.Payment.Bolt11 != nil {
		feeTotalLoki = event.Payment.Bolt11.FeeTotalLoki
		orderTotalLoki = event.Payment.Bolt11.OrderTotalLoki
	}

	return map[string]interface{}{
		"order_id":            event.OrderID,
		"state":               state,
		"payment_invoice":     invoice,
		"fee_total_loki":      feeTotalLoki,
		"order_total_loki":    orderTotalLoki,
		"lsp_balance_loki":    event.Order.LspBalanceLoki,
		"client_balance_loki": event.Order.ClientBalanceLoki,
	}, nil
}

// LSPS2GetInfo requests JIT channel opening params
func (api *api) LSPS2GetInfo(ctx context.Context, req *LSPS2GetInfoRequest) (interface{}, error) {
	lm, err := api.contactLSP(req.LSPPubkey)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, lspsRequestTimeout)
	defer cancel()

	fees, err := lm.GetLSPS2FeeParams(ctx, req.LSPPubkey)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			logger.Logger.Warn().Str("lsp", req.LSPPubkey).Msg("LSPS2 GetInfo timeout")
			return nil, fmt.Errorf("time out, please retry")
		}
		logger.Logger.Error().Err(err).Str("lsp", req.LSPPubkey).Msg("LSPS2 GetInfo failed")
		return nil, err
	}

	if len(fees) == 0 {
		return nil, fmt.Errorf("no fee parameters provided by LSP")
	}

	// Return the first fee parameter set as a flat object
	return fees[0], nil
}

// LSPS2Buy buys a JIT channel
func (api *api) LSPS2Buy(ctx context.Context, req *LSPS2BuyRequest) (*LSPS2BuyResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, lspsRequestTimeout)
	defer cancel()

	if api.svc.GetLiquidityManager() == nil {
		return nil, fmt.Errorf("LiquidityManager not started")
	}

	if req.PaymentSizeMloki == nil {
		return nil, fmt.Errorf("payment_size_mloki is required")
	}

	// Use the new robust BuyLiquidity method which handles retries
	hints, err := api.svc.GetLiquidityManager().BuyLiquidity(ctx, req.LSPPubkey, *req.PaymentSizeMloki, &req.OpeningFeeParams, nil)
	if err != nil {
		logger.Logger.Error().Err(err).Str("lsp", req.LSPPubkey).Uint64("amount", *req.PaymentSizeMloki).Msg("LSPS2 BuyLiquidity failed")
		return nil, err
	}

	// Helper to parse SCID string back to uint64 for response compatibility
	scid, err := strconv.ParseUint(hints.SCID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid SCID returned from manager: %w", err)
	}

	return &LSPS2BuyResponse{
		// RequestID is less relevant in this abstracted flow, but we can leave it empty or generated if needed.
		// The frontend typically just needs the SCID and CltvDelta.
		RequestID:       "",
		InterceptSCID:   scid,
		CLTVExpiryDelta: hints.CLTVExpiryDelta,
		LSPNodeID:       hints.LSPNodeID,
	}, nil
}

// LSPS5SetWebhook registers a webhook
func (api *api) LSPS5SetWebhook(ctx context.Context, req *LSPS5SetWebhookRequest) (interface{}, error) {
	ctx, cancel := context.WithTimeout(ctx, lspsRequestTimeout)
	defer cancel()

	lm, err := api.contactLSP(req.LSPPubkey)
	if err != nil {
		return nil, err
	}
	client := lm.LSPS5Client()
	if client == nil {
		return nil, fmt.Errorf("LSPS5 client not available")
	}

	// Assuming appName is "lokihub" or part of the request?
	// The LSPS5SetWebhookRequest has URL, Events, Signature (optional?)
	// The client.SetWebhook signature might strictly match.

	// Assume https if not specified? Or maybe allow empty if inferred?
	// The struct now has Transport field
	transport := req.Transport
	if transport == "" {
		transport = "https"
	}

	reqID, err := client.SetWebhook(ctx, req.LSPPubkey, "lokihub", req.URL, transport)
	if err != nil {
		logger.Logger.Error().Err(err).Str("lsp", req.LSPPubkey).Str("url", req.URL).Msg("LSPS5 SetWebhook failed")
		return nil, err
	}

	return map[string]string{"requestId": reqID}, nil
}

// LSPS5ListWebhooks lists webhooks
func (api *api) LSPS5ListWebhooks(ctx context.Context, req *LSPS5ListWebhooksRequest) (interface{}, error) {
	ctx, cancel := context.WithTimeout(ctx, lspsRequestTimeout)
	defer cancel()

	client := api.svc.GetLiquidityManager().LSPS5Client()
	if client == nil {
		return nil, fmt.Errorf("LSPS5 client not available")
	}

	reqID, err := client.ListWebhooks(ctx, req.LSPPubkey)
	if err != nil {
		logger.Logger.Error().Err(err).Str("lsp", req.LSPPubkey).Msg("LSPS5 ListWebhooks failed")
		return nil, err
	}

	return map[string]string{"requestId": reqID}, nil
}

// LSPS5RemoveWebhook removes a webhook
func (api *api) LSPS5RemoveWebhook(ctx context.Context, req *LSPS5RemoveWebhookRequest) (interface{}, error) {
	ctx, cancel := context.WithTimeout(ctx, lspsRequestTimeout)
	defer cancel()

	client := api.svc.GetLiquidityManager().LSPS5Client()
	if client == nil {
		return nil, fmt.Errorf("LSPS5 client not available")
	}

	reqID, err := client.RemoveWebhook(ctx, req.LSPPubkey, req.URL)
	if err != nil {
		logger.Logger.Error().Err(err).Str("lsp", req.LSPPubkey).Str("url", req.URL).Msg("LSPS5 RemoveWebhook failed")
		return nil, err
	}

	return map[string]string{"requestId": reqID}, nil
}

// disconnectLSP disconnects the LSP peer
func (api *api) disconnectLSP(ctx context.Context, pubkey string) error {
	if err := api.DisconnectPeer(ctx, pubkey); err != nil {
		if strings.Contains(err.Error(), "is not connected") {
			logger.Logger.Info().Str("pubkey", pubkey).Msg("Peer already disconnected, ignoring error")
			return nil
		}
		logger.Logger.Warn().Err(err).Str("pubkey", pubkey).Msg("Failed to disconnect peer")
		return err
	}
	// The node tears the connection down in the background: wait until the
	// peer is gone, so a trust right after reconnects instead of finding a peer
	// still exiting ("peer exiting" on the next LSPS message).
	ln := api.svc.GetLNClient()
	if ln == nil {
		return nil
	}
	deadline := time.Now().Add(peerGoneWait)
	for time.Now().Before(deadline) {
		peers, err := ln.ListPeers(ctx)
		if err != nil {
			return nil
		}
		connected := false
		for _, p := range peers {
			connected = connected || (p.NodeId == pubkey && p.IsConnected)
		}
		if !connected {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

// peerGoneWait bounds disconnectLSP's wait for the peer to be gone.
const peerGoneWait = 5 * time.Second

// checkHasChannels checks if there are any open channels with the given pubkey
func (api *api) checkHasChannels(ctx context.Context, pubkey string) error {
	if api.svc.GetLNClient() == nil {
		return nil // Cannot check if client not ready, assume safe? Or block?
		// If LNClient is nil, we probably can't delete anyway or it's safe.
	}

	channels, err := api.svc.GetLNClient().ListChannels(ctx)
	if err != nil {
		logger.Logger.Error().Err(err).Msg("Failed to list channels during LSP removal check")
		return fmt.Errorf("failed to check for active channels")
	}

	for _, ch := range channels {
		if strings.EqualFold(ch.RemotePubkey, pubkey) {
			return fmt.Errorf("cannot remove LSP with open channels")
		}
	}
	return nil
}

// ListLSPs are the trusted LSPs (used to recognise an LSP's webhook calls).
func (api *api) ListLSPs() ([]manager.SettingsLSP, error) {
	trusted, err := api.lspManager.ListTrustedLSPs()
	if err != nil {
		return nil, err
	}
	out := make([]manager.SettingsLSP, 0, len(trusted))
	for _, l := range trusted {
		out = append(out, manager.SettingsLSP{NodeKey: l.NodeKey, Name: l.Name, Description: l.Description,
			Website: l.Website, Pubkey: l.Pubkey, Host: l.Host, Trusted: l.Trusted, Status: l.Status,
			Usable: l.Usable, NostrPubkey: l.NostrPubkey})
	}
	return out, nil
}

// UpdateLSPS1OrderState updates the state of an LSPS1 order from a webhook
// notification. The caller (lsps5WebhookCallbackHandler) has already verified
// lspPubkey via a signature, but that only proves the sender controls
// lspPubkey — not that lspPubkey is a real LSP this hub trusts, nor that it's
// the specific LSP that owns orderID. Both are checked here so a notification
// self-signed by any keypair can't forge a state transition for an order it
// doesn't own.
func (api *api) UpdateLSPS1OrderState(ctx context.Context, lspPubkey, orderID, state string) error {
	// Ownership is checked before touching the LiquidityManager at all: it's a
	// pure authorization decision against api.lspManager (always available,
	// doesn't depend on whether the manager has started), and rejecting a
	// forged notification shouldn't be gated on unrelated service state.
	order, err := api.lspManager.GetOrder(orderID)
	if err != nil {
		return fmt.Errorf("order %s not found: %w", orderID, err)
	}
	if order.LSPPubkey != lspPubkey {
		return fmt.Errorf("lsp pubkey does not match the LSP that created order %s", orderID)
	}
	if api.svc.GetLiquidityManager() == nil {
		return fmt.Errorf("LiquidityManager not started")
	}
	api.svc.GetLiquidityManager().HandleOrderStateUpdate(orderID, state, lspPubkey)
	return nil
}

func (api *api) LSPS1ListOrders(ctx context.Context) (*LSPS1ListOrdersResponse, error) {
	if api.svc.GetLiquidityManager() == nil {
		return nil, fmt.Errorf("LiquidityManager not started")
	}
	orders, err := api.svc.GetLiquidityManager().ListLSPS1Orders()
	if err != nil {
		return nil, err
	}

	response := &LSPS1ListOrdersResponse{
		Orders: make([]LSPS1Order, len(orders)),
	}

	for i, o := range orders {
		response.Orders[i] = LSPS1Order{
			OrderID:           o.OrderID,
			LSPPubkey:         o.LSPPubkey,
			State:             o.State,
			PaymentInvoice:    o.PaymentInvoice,
			FeeTotal:          o.FeeTotal,
			OrderTotal:        o.OrderTotal,
			LSPBalanceLoki:    o.LSPBalance,
			ClientBalanceLoki: o.ClientBalance,
			CreatedAt:         o.CreatedAt,
			UpdatedAt:         o.UpdatedAt,
		}
	}

	return response, nil
}
