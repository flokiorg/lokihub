package itest

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/lspd/lsps"
)

// Lsps1Client drives the buyer side of an LSPS1 manual channel-order
// purchase against a single LSP peer, exactly as a wallet client would --
// the counterpart to Lsps2Client for the non-JIT flow.
type Lsps1Client struct {
	bus       *CustomMessageBus
	lspPubkey []byte
}

func NewLsps1Client(bus *CustomMessageBus, lspPubkeyHex string) (*Lsps1Client, error) {
	pk, err := hex.DecodeString(lspPubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("decoding LSP pubkey: %w", err)
	}
	return &Lsps1Client{bus: bus, lspPubkey: pk}, nil
}

// GetInfo fetches the LSP's LSPS1 options/limits.
func (c *Lsps1Client) GetInfo(ctx context.Context) (*lsps.Lsps1GetInfoResponse, error) {
	result, err := c.bus.Call(ctx, c.lspPubkey, "lsps1.get_info", struct{}{}, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var resp lsps.Lsps1GetInfoResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("unmarshaling get_info result: %w", err)
	}
	return &resp, nil
}

// CreateOrder places a manual LSPS1 order for a channel with lspBalanceLoki
// inbound liquidity (clientBalanceLoki push, typically 0). The LSP's
// response includes a bolt11 invoice: paying it (via any route reaching
// the LSP, not a JIT/phantom hop -- this is an ordinary invoice, unlike
// LSPS2's) is what triggers the LSP's settlement service to open the
// channel once the payment settles.
func (c *Lsps1Client) CreateOrder(ctx context.Context, token string, lspBalanceLoki, clientBalanceLoki uint64) (*lsps.Lsps1CreateOrderResponse, error) {
	req := lsps.Lsps1CreateOrderRequest{
		LspBalanceLoki:               fmt.Sprintf("%d", lspBalanceLoki),
		ClientBalanceLoki:            fmt.Sprintf("%d", clientBalanceLoki),
		RequiredChannelConfirmations: 1,
		FundingConfirmsWithinBlocks:  1,
		ChannelExpiryBlocks:          1000,
		Token:                        token,
		AnnounceChannel:              false,
	}
	result, err := c.bus.Call(ctx, c.lspPubkey, "lsps1.create_order", req, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var resp lsps.Lsps1CreateOrderResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("unmarshaling create_order result: %w", err)
	}
	return &resp, nil
}

// GetOrder polls the current state of a previously created order.
func (c *Lsps1Client) GetOrder(ctx context.Context, orderId string) (*lsps.Lsps1CreateOrderResponse, error) {
	result, err := c.bus.Call(ctx, c.lspPubkey, "lsps1.get_order", lsps.Lsps1GetOrderRequest{OrderId: orderId}, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var resp lsps.Lsps1CreateOrderResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("unmarshaling get_order result: %w", err)
	}
	return &resp, nil
}
