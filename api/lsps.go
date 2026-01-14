package api

import (
	"context"
	"fmt"

	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/manager"
)

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
	client := api.svc.GetLiquidityManager().LSPS1Client()
	if client == nil {
		return nil, fmt.Errorf("LSPS1 client not available")
	}

	// This assumes synchronous request/response handling wrapper in client or manual handling here
	// The current ClientHandler returns a RequestID, and we need to wait for the event.
	// For simplicity in this first pass, we might need a way to wait for the result
	// or we return the RequestID and let the frontend poll/subscribe.
	// However, standard API expectation is request/response.

	// TODO: Implement synchronous wrapper or event waiting mechanism
	// For now, let's just trigger the request and return the RequestID
	// But the interface signature returns (interface{}, error)
	// We should probably start with returning RequestID or similar.

	// Actually, the Rust client and our Go client return (string, error) where string is requestID.
	reqID, err := client.RequestSupportedOptions(ctx, req.LSPPubkey)
	if err != nil {
		return nil, err
	}

	return map[string]string{"requestId": reqID}, nil
}

// LSPS1CreateOrder creates a channel order
func (api *api) LSPS1CreateOrder(ctx context.Context, req *LSPS1CreateOrderRequest) (interface{}, error) {
	client := api.svc.GetLiquidityManager().LSPS1Client()
	if client == nil {
		return nil, fmt.Errorf("LSPS1 client not available")
	}

	orderParams := lsps1.OrderParams{
		LspBalanceLoki:      req.LSPBalanceLoki,
		ChannelExpiryBlocks: req.ChannelExpiryBlocks,
		Token:               req.Token,
		AnnounceChannel:     req.AnnounceChannel,
	}

	reqID, err := client.CreateOrder(ctx, req.LSPPubkey, orderParams, req.RefundOnchainAddress)
	if err != nil {
		return nil, err
	}

	return map[string]string{"requestId": reqID}, nil
}

// LSPS1GetOrder checks order status
func (api *api) LSPS1GetOrder(ctx context.Context, req *LSPS1GetOrderRequest) (interface{}, error) {
	client := api.svc.GetLiquidityManager().LSPS1Client()
	if client == nil {
		return nil, fmt.Errorf("LSPS1 client not available")
	}

	reqID, err := client.CheckOrderStatus(ctx, req.LSPPubkey, req.OrderID)
	if err != nil {
		return nil, err
	}

	return map[string]string{"requestId": reqID}, nil
}

// LSPS2GetInfo requests JIT channel opening params
func (api *api) LSPS2GetInfo(ctx context.Context, req *LSPS2GetInfoRequest) (interface{}, error) {
	if api.svc.GetLiquidityManager() == nil {
		return nil, fmt.Errorf("LiquidityManager not started")
	}

	fees, err := api.svc.GetLiquidityManager().GetLSPS2FeeParams(ctx, req.LSPPubkey)
	if err != nil {
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

// ListLSPs returns all configured LSPs
func (api *api) ListLSPs() ([]manager.SettingsLSP, error) {
	lm := api.svc.GetLiquidityManager()
	if lm == nil {
		return nil, fmt.Errorf("LiquidityManager not available")
	}
	return lm.GetLSPs()
}

// GetSelectedLSPs returns selected LSPs
func (api *api) GetSelectedLSPs() ([]manager.SettingsLSP, error) {
	lm := api.svc.GetLiquidityManager()
	if lm == nil {
		return nil, fmt.Errorf("LiquidityManager not available")
	}
	return lm.GetSelectedLSPs()
}

// AddSelectedLSP adds an LSP to the selected list
func (api *api) AddSelectedLSP(pubkey string) error {
	lm := api.svc.GetLiquidityManager()
	if lm == nil {
		return fmt.Errorf("LiquidityManager not available")
	}
	return lm.AddSelectedLSP(pubkey)
}

// RemoveSelectedLSP removes an LSP from the selected list
func (api *api) RemoveSelectedLSP(pubkey string) error {
	lm := api.svc.GetLiquidityManager()
	if lm == nil {
		return fmt.Errorf("LiquidityManager not available")
	}
	return lm.RemoveSelectedLSP(pubkey)
}

// AddLSP adds a new LSP to configuration
func (api *api) AddLSP(name, uri string) error {
	lm := api.svc.GetLiquidityManager()
	if lm == nil {
		return fmt.Errorf("LiquidityManager not available")
	}
	return lm.AddLSP(name, uri)
}

// RemoveLSP removes an LSP from configuration
func (api *api) RemoveLSP(pubkey string) error {
	lm := api.svc.GetLiquidityManager()
	if lm == nil {
		return fmt.Errorf("LiquidityManager not available")
	}
	return lm.RemoveLSP(pubkey)
}
