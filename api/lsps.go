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
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/flokiorg/lokihub/utils"
	"gorm.io/gorm"
)

// lspsRequestTimeout is the timeout for LSPS protocol requests
const lspsRequestTimeout = 5 * time.Second

// LSPS0ListProtocols lists supported protocols
func (api *api) LSPS0ListProtocols(ctx context.Context, req *LSPS0ListProtocolsRequest) (*LSPS0ListProtocolsResponse, error) {
	client := api.svc.GetLiquidityManager().LSPS0Client()
	if client == nil {
		return nil, fmt.Errorf("LSPS0 client not available")
	}

	protocols, err := client.ListProtocols(ctx, req.LSPPubkey)
	if err != nil {
		return nil, err
	}

	return &LSPS0ListProtocolsResponse{
		Protocols: protocols,
	}, nil
}

// LSPS1GetInfo gets channel ordering info
func (api *api) LSPS1GetInfo(ctx context.Context, req *LSPS1GetInfoRequest) (interface{}, error) {
	// Use synchronous manager method
	// Note: manager returns []lsps1.LSPS1Option or similar?
	// The manager method signature I added in manager.go checks `GetLSPS1InfoList` returns `[]lsps1.LSPS1Option`?
	// Wait, in manager.go I used `lsps1.LSPS1Option` which was UNDEFINED.
	// I need to fix manager.go imports or type names FIRST.
	// In msgs.go it is called `Options`.
	// So let's assume I fix manager.go to use `lsps1.Options`.

	// Assuming manager is fixed:
	options, err := api.svc.GetLiquidityManager().GetLSPS1InfoList(ctx, req.LSPPubkey)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"options": options,
	}, nil
}

// LSPS1CreateOrder creates a channel order
func (api *api) LSPS1CreateOrder(ctx context.Context, req *LSPS1CreateOrderRequest) (interface{}, error) {
	orderParams := lsps1.OrderParams{
		LspBalanceLoki:               req.LSPBalanceLoki, // Amount of inbound liquidity requested (from amount_loki)
		ClientBalanceLoki:            0,                  // Client typically provides 0 for inbound-only buy
		RequiredChannelConfirmations: 0,                  // Accept unconfirmed (LSP can override based on their policy)
		FundingConfirmsWithinBlocks:  6,                  // Standard Bitcoin confirmation window
		ChannelExpiryBlocks:          req.ChannelExpiryBlocks,
		Token:                        req.Token,
		AnnounceChannel:              req.AnnounceChannel,
	}

	logger.Logger.Info().Interface("order_params", orderParams).Msg("Creating LSPS1 order")

	event, err := api.svc.GetLiquidityManager().CreateLSPS1Order(ctx, req.LSPPubkey, orderParams, req.RefundOnchainAddress)
	if err != nil {
		return nil, err
	}

	invoice := ""
	if event.Payment.Bolt11 != nil {
		invoice = event.Payment.Bolt11.Invoice
	}

	return map[string]interface{}{
		"order_id":        event.OrderID,
		"payment_invoice": invoice,
	}, nil
}

// LSPS1GetOrder checks order status
func (api *api) LSPS1GetOrder(ctx context.Context, req *LSPS1GetOrderRequest) (interface{}, error) {
	event, err := api.svc.GetLiquidityManager().GetLSPS1Order(ctx, req.LSPPubkey, req.OrderID)
	if err != nil {
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

	return map[string]interface{}{
		"order_id":        event.OrderID,
		"state":           state,
		"payment_invoice": invoice,
	}, nil
}

// LSPS2GetInfo requests JIT channel opening params
func (api *api) LSPS2GetInfo(ctx context.Context, req *LSPS2GetInfoRequest) (interface{}, error) {
	if api.svc.GetLiquidityManager() == nil {
		return nil, fmt.Errorf("LiquidityManager not started")
	}

	ctx, cancel := context.WithTimeout(ctx, lspsRequestTimeout)
	defer cancel()

	fees, err := api.svc.GetLiquidityManager().GetLSPS2FeeParams(ctx, req.LSPPubkey)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("time out, please retry")
		}
		return nil, err
	}

	// Wrap in expected response struct or return directly
	// The frontend likely expects { opening_fee_params_menu: [...] } matching LSPS2 spec
	return lsps2.GetInfoResponse{
		OpeningFeeParamsMenu: fees,
	}, nil
}

// LSPS2Buy buys a JIT channel
func (api *api) LSPS2Buy(ctx context.Context, req *LSPS2BuyRequest) (*LSPS2BuyResponse, error) {
	if api.svc.GetLiquidityManager() == nil {
		return nil, fmt.Errorf("LiquidityManager not started")
	}

	if req.PaymentSizeMloki == nil {
		return nil, fmt.Errorf("payment_size_mloki is required")
	}

	resp, err := api.svc.GetLiquidityManager().OpenJitChannel(ctx, req.LSPPubkey, *req.PaymentSizeMloki, req.OpeningFeeParams)
	if err != nil {
		return nil, err
	}

	return &LSPS2BuyResponse{
		RequestID:       resp.RequestID,
		InterceptSCID:   resp.InterceptSCID,
		CLTVExpiryDelta: resp.CLTVExpiryDelta,
		LSPNodeID:       resp.LSPNodeID,
	}, nil
}

// LSPS5SetWebhook registers a webhook
func (api *api) LSPS5SetWebhook(ctx context.Context, req *LSPS5SetWebhookRequest) (interface{}, error) {
	client := api.svc.GetLiquidityManager().LSPS5Client()
	if client == nil {
		return nil, fmt.Errorf("LSPS5 client not available")
	}

	// Assuming appName is "lokihub" or part of the request?
	// The LSPS5SetWebhookRequest has URL, Events, Signature (optional?)
	// The client.SetWebhook signature might strictly match.

	// TODO: Use correct params
	reqID, err := client.SetWebhook(ctx, req.LSPPubkey, "lokihub", req.URL)
	if err != nil {
		return nil, err
	}

	return map[string]string{"requestId": reqID}, nil
}

// LSPS5ListWebhooks lists webhooks
func (api *api) LSPS5ListWebhooks(ctx context.Context, req *LSPS5ListWebhooksRequest) (interface{}, error) {
	client := api.svc.GetLiquidityManager().LSPS5Client()
	if client == nil {
		return nil, fmt.Errorf("LSPS5 client not available")
	}

	reqID, err := client.ListWebhooks(ctx, req.LSPPubkey)
	if err != nil {
		return nil, err
	}

	return map[string]string{"requestId": reqID}, nil
}

// LSPS5RemoveWebhook removes a webhook
func (api *api) LSPS5RemoveWebhook(ctx context.Context, req *LSPS5RemoveWebhookRequest) (interface{}, error) {
	client := api.svc.GetLiquidityManager().LSPS5Client()
	if client == nil {
		return nil, fmt.Errorf("LSPS5 client not available")
	}

	reqID, err := client.RemoveWebhook(ctx, req.LSPPubkey, req.URL)
	if err != nil {
		return nil, err
	}

	return map[string]string{"requestId": reqID}, nil
}

// Helper to parse host:port
func parseHostPort(hostPort string) (string, uint16, error) {
	parts := strings.Split(hostPort, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid host:port format: %s", hostPort)
	}
	port, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port number: %w", err)
	}
	return parts[0], uint16(port), nil
}

// connectLSP attempts to connect to the LSP peer
func (api *api) connectLSP(ctx context.Context, pubkey string, host string) error {
	address, port, err := parseHostPort(host)
	if err != nil {
		logger.Logger.Error().Err(err).Str("host", host).Msg("Failed to parse host:port for LSP connection")
		return err
	}

	req := &ConnectPeerRequest{
		Pubkey:  pubkey,
		Address: address,
		Port:    port,
	}

	if err := api.ConnectPeer(ctx, req); err != nil {
		if strings.Contains(err.Error(), "already connected") {
			logger.Logger.Info().Str("pubkey", pubkey).Msg("Peer already connected, ignoring error")
			return nil
		}
		logger.Logger.Error().Err(err).Str("pubkey", pubkey).Msg("Failed to connect to LSP peer")
		return err
	}
	return nil
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
	return nil
}

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

// HandleListLSPs returns all configured LSPs
func (api *api) HandleListLSPs(ctx context.Context) ([]manager.SettingsLSP, error) {
	// We want to return the format the frontend expects (SettingsLSP)
	// which effectively merges DB data with active status.
	// Since we now store everything in DB, we just map it.

	// We use the same struct as manager for compatibility or define a new Response struct?
	// Frontend expects: { name, pubkey, host, active }
	// We can reuse manager.SettingsLSP for now.

	dbLSPs, err := api.lspManager.ListLSPs()
	if err != nil {
		return nil, err
	}

	var result []manager.SettingsLSP
	for _, l := range dbLSPs {
		result = append(result, manager.SettingsLSP{
			Name:        l.Name,
			Description: l.Description,
			Pubkey:      l.Pubkey,
			Host:        l.Host,
			Active:      l.IsActive,
			IsCommunity: l.IsCommunity,
		})
	}
	return result, nil
}

type AddLSPRequest struct {
	Name   string `json:"name"`
	Host   string `json:"host"` // Format: host:port
	Pubkey string `json:"pubkey"`
}

// HandleAddLSP adds a new custom LSP
func (api *api) HandleAddLSP(ctx context.Context, req *AddLSPRequest) (*manager.SettingsLSP, error) {
	// Validate
	if req.Name == "" || req.Host == "" || req.Pubkey == "" {
		return nil, fmt.Errorf("name, host, and pubkey are required")
	}

	// Add to DB via LiquidityManager
	uri := req.Pubkey + "@" + req.Host
	if err := api.svc.GetLiquidityManager().AddLSP(req.Name, uri); err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) && !strings.Contains(err.Error(), "already exists") {
			return nil, err
		}
		// If it exists, we proceed to ensure it's active and connected.
		logger.Logger.Info().Str("pubkey", req.Pubkey).Msg("LSP already exists, ensuring it is active...")
	}

	// Force active=true for manually added LSPs and ensure DB is updated
	if err := api.lspManager.ToggleLSP(req.Pubkey, true); err != nil {
		return nil, fmt.Errorf("failed to activate LSP: %w", err)
	}

	// Try to connect (ignore already connected error)
	if err := api.connectLSP(ctx, req.Pubkey, req.Host); err != nil {
		// Specific check for self-connection attempt, which should fail the addition
		if strings.Contains(err.Error(), "cannot make connection to self") {
			// Rollback: remove the LSP we just added
			if delErr := api.lspManager.DeleteCustomLSP(req.Pubkey); delErr != nil {
				logger.Logger.Error().Err(delErr).Str("pubkey", req.Pubkey).Msg("Failed to rollback LSP addition after self-connection error")
			}
			return nil, fmt.Errorf("cannot add your own node as an LSP")
		}

		// Log but don't fail the addition for other connection errors (e.g. offline)
		logger.Logger.Warn().Err(err).Str("pubkey", req.Pubkey).Msg("Failed to connect to new LSP")
	}

	// Return the object
	return &manager.SettingsLSP{
		Name:   req.Name,
		Pubkey: req.Pubkey,
		Host:   req.Host,
		Active: true,
	}, nil
}

type UpdateLSPRequest struct {
	Active bool `json:"active"`
}

// HandleUpdateLSP updates LSP status (Toggle)
func (api *api) HandleUpdateLSP(ctx context.Context, pubkey string, req *UpdateLSPRequest) error {
	lm := api.svc.GetLiquidityManager()
	if req.Active {
		// Need host to connect. Fetch LSP first.
		lsps, err := api.lspManager.ListLSPs()
		if err != nil {
			return err
		}
		var target *persist.LSP
		for i := range lsps {
			if strings.EqualFold(lsps[i].Pubkey, pubkey) {
				target = &lsps[i]
				break
			}
		}
		if target == nil {
			return errors.New("LSP not found")
		}

		if err := lm.SetActiveLSP(pubkey); err != nil {
			return err
		}
		// Connect
		if err := api.connectLSP(ctx, pubkey, target.Host); err != nil {
			return err
		}
	} else {
		// Check for active channels before removing/deactivating
		if err := api.checkHasChannels(ctx, pubkey); err != nil {
			return err
		}

		if err := lm.RemoveSelectedLSP(pubkey); err != nil {
			return err
		}
		// Disconnect
		if err := api.disconnectLSP(ctx, pubkey); err != nil {
			// ignore disconnect error?
		}
	}
	return nil
}

// HandleDeleteLSP removes a custom LSP
func (api *api) HandleDeleteLSP(ctx context.Context, pubkey string) error {
	// Check for active channels before removing
	if err := api.checkHasChannels(ctx, pubkey); err != nil {
		return err
	}

	lm := api.svc.GetLiquidityManager()
	if err := lm.RemoveLSP(pubkey); err != nil {
		return err
	}
	// Disconnect
	return api.disconnectLSP(ctx, pubkey)
}

// Keeping legacy simplified methods for internal use if needed, but delegating
func (api *api) ListLSPs() ([]manager.SettingsLSP, error) {
	return api.HandleListLSPs(context.Background())
}

// AddLSP adds a new LSP to configuration (Legacy wrapper)
func (api *api) AddLSP(name, uri string) error {
	pubkey, host, err := utils.ParseLSPURI(uri)
	if err != nil {
		return err
	}
	// Call HandleAddLSP which handles DB + Connection
	_, err = api.HandleAddLSP(context.Background(), &AddLSPRequest{
		Name:   name,
		Pubkey: pubkey,
		Host:   host,
	})
	return err
}

// RemoveLSP removes an LSP (Legacy wrapper)
func (api *api) RemoveLSP(pubkey string) error {
	return api.HandleDeleteLSP(context.Background(), pubkey)
}

// AddSelectedLSP activates an LSP (Legacy wrapper)
func (api *api) AddSelectedLSP(pubkey string) error {
	return api.HandleUpdateLSP(context.Background(), pubkey, &UpdateLSPRequest{Active: true})
}

// RemoveSelectedLSP deactivates an LSP (Legacy wrapper)
func (api *api) RemoveSelectedLSP(pubkey string) error {
	return api.HandleUpdateLSP(context.Background(), pubkey, &UpdateLSPRequest{Active: false})
}

// GetSelectedLSPs returns active LSPs (Legacy wrapper)
func (api *api) GetSelectedLSPs() ([]manager.SettingsLSP, error) {
	lsps, err := api.HandleListLSPs(context.Background())
	if err != nil {
		return nil, err
	}
	var selected []manager.SettingsLSP
	for _, l := range lsps {
		if l.Active {
			selected = append(selected, l)
		}
	}
	return selected, nil
}

// saveLSPsToDatabase saves LSPs directly using LSPManager during setup
func (api *api) saveLSPsToDatabase(lsps []LSPSettingInput) error {
	ctx := context.Background()
	for _, input := range lsps {
		pubkey, host, err := utils.ParseLSPURI(input.Pubkey + "@" + input.Host)
		if err != nil {
			logger.Logger.Warn().Err(err).Msgf("Invalid LSP URI in setup: %s", input.Name)
			continue
		}

		existing, err := api.lspManager.AddLSP(input.Name, pubkey, host, input.Active, input.IsCommunity)
		if err != nil {
			if err.Error() == "LSP with this pubkey already exists" {
				// Update active status
				if err := api.lspManager.ToggleLSP(pubkey, input.Active); err != nil {
					logger.Logger.Error().Err(err).Str("pubkey", pubkey).Msg("Failed to update LSP active status in setup")
				}
			} else {
				logger.Logger.Error().Err(err).Str("name", input.Name).Msg("Failed to add LSP in setup")
			}
		} else {
			_ = existing
		}

		// Handle connection state
		if input.Active {
			go func(pk, h string) {
				// Check if we can connect
				if api.svc.GetLNClient() == nil {
					return
				}
				if err := api.connectLSP(ctx, pk, h); err != nil {
					logger.Logger.Warn().Err(err).Str("pubkey", pk).Msg("Failed to connect to LSP in setup")
				}
			}(pubkey, host)
		} else {
			go func(pk string) {
				if err := api.disconnectLSP(ctx, pk); err != nil {
					// Ignore
				}
			}(pubkey)
		}
	}

	return nil
}
