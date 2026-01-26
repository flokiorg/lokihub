package service

import (
	"context"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/logger"
)

type lspsEventConsumer struct {
	svc *service
}

func (c *lspsEventConsumer) ConsumeEvent(ctx context.Context, event *events.Event, globalProperties map[string]interface{}) {
	if event.Event == constants.LSPS5_EVENT_PAYMENT_INCOMING {
		c.handlePaymentIncoming(ctx, event)
	} else if event.Event == constants.LSPS5_EVENT_ORDER_STATE_CHANGED {
		c.handleOrderStateChanged(ctx, event)
	}
}

func (c *lspsEventConsumer) handleOrderStateChanged(ctx context.Context, event *events.Event) {
	props, ok := event.Properties.(map[string]interface{})
	if !ok {
		return
	}

	state, _ := props["state"].(string)
	orderID, _ := props["order_id"].(string)
	lspPubkey, _ := props["lsp_pubkey"].(string)

	if state == "FAILED" {
		errMsg, _ := props["error"].(string)
		logger.Logger.Error().
			Str("order_id", orderID).
			Str("lsp", lspPubkey).
			Str("error", errMsg).
			Msg("LSPS order failed terminal status")

		// Here we could trigger internal reconciliation/UI updates
	} else if state == "COMPLETED" {
		channelPoint, _ := props["channel_point"].(string)
		logger.Logger.Info().
			Str("order_id", orderID).
			Str("lsp", lspPubkey).
			Str("channel_point", channelPoint).
			Msg("LSPS order completed successfully")
	}
}

func (c *lspsEventConsumer) handlePaymentIncoming(ctx context.Context, event *events.Event) {
	props, ok := event.Properties.(map[string]interface{})
	if !ok {
		return
	}

	lspPubkey, ok := props["lsp_pubkey"].(string)
	if !ok || lspPubkey == "" {
		logger.Logger.Warn().Interface("event", event).Msg("LSPS5 payment notification missing lsp_pubkey")
		return
	}

	logger.Logger.Info().Str("lsp", lspPubkey).Msg("Received JIT payment notification, ensuring connection to LSP")

	// Trigger connection to LSP to facilitate JIT channel opening
	// We run this in a goroutine to not block the event loop, although this consumer is likely already async?
	// The event publisher usually calls consumers synchronously or async depending on implementation.
	// Safe to just call it.

	// Use background context for connection as event context might be short-lived?
	// Actually, usually passed context is valid.
	if err := c.svc.GetLiquidityManager().ConnectLSP(context.Background(), lspPubkey); err != nil {
		logger.Logger.Error().Err(err).Str("lsp", lspPubkey).Msg("Failed to connect to LSP after notification")
	}
}
