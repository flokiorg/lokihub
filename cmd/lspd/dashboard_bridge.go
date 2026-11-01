package main

import (
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/notifications"
	transdashboard "github.com/flokiorg/lokihub/lspd/transport/dashboard"
)

type DashboardNotificationBridge struct {
	inner   *notifications.NotificationService
	dashSvc *transdashboard.Server
}

func NewDashboardNotificationBridge(inner *notifications.NotificationService, dashSvc *transdashboard.Server) *DashboardNotificationBridge {
	return &DashboardNotificationBridge{
		inner:   inner,
		dashSvc: dashSvc,
	}
}

func (b *DashboardNotificationBridge) Notify(pubkey string, paymenthash string, nodePrivKey interface{}) (bool, error) {
	// 1. Deliver to LSPS5 webhooks
	res, err := b.inner.Notify(pubkey, paymenthash, nodePrivKey)

	// 2. Broadcast to Dashboard WebSocket
	b.dashSvc.BroadcastEvent("payment_incoming", map[string]interface{}{
		"pubkey":       pubkey,
		"payment_hash": paymenthash,
	})

	return res, err
}

func (b *DashboardNotificationBridge) NotifyAsync(pubkey string, paymenthash string, nodePrivKey interface{}) {
	// 1. Deliver to LSPS5 webhooks (durably enqueued, not waited on -- see
	// NotificationService.NotifyAsync's doc comment)
	b.inner.NotifyAsync(pubkey, paymenthash, nodePrivKey)

	// 2. Broadcast to Dashboard WebSocket -- a real-time "payment incoming"
	// event for the operator is worth surfacing regardless of which path
	// (sync or async) handled the LSPS5 webhook side, so this mirrors
	// Notify's own broadcast rather than skipping it.
	b.dashSvc.BroadcastEvent("payment_incoming", map[string]interface{}{
		"pubkey":       pubkey,
		"payment_hash": paymenthash,
	})
}

func (b *DashboardNotificationBridge) NotifyWebhookRegistered(pubkey string, webhook *core.Lsps5Webhook) bool {
	return b.inner.NotifyWebhookRegistered(pubkey, webhook)
}

func (b *DashboardNotificationBridge) NotifyOrderStateChanged(pubkey string, orderId string, state string, channelPoint string, errorMsg string) (bool, error) {
	// 1. Deliver to LSPS5 webhooks
	res, err := b.inner.NotifyOrderStateChanged(pubkey, orderId, state, channelPoint, errorMsg)

	// 2. Broadcast to Dashboard WebSocket
	b.dashSvc.BroadcastEvent("order_state_changed", map[string]interface{}{
		"order_id":      orderId,
		"state":         state,
		"channel_point": channelPoint,
		"error":         errorMsg,
	})

	return res, err
}

func (b *DashboardNotificationBridge) NotifyExpirySoon(pubkey string, timeout int, nodePrivKey interface{}, paymentHash string) (bool, error) {
	res, err := b.inner.NotifyExpirySoon(pubkey, timeout, nodePrivKey, paymentHash)

	b.dashSvc.BroadcastEvent("expiry_soon", map[string]interface{}{
		"pubkey":       pubkey,
		"timeout":      timeout,
		"payment_hash": paymentHash,
	})

	return res, err
}

func (b *DashboardNotificationBridge) NotifyLiquidityManagementRequest(pubkey string, nodePrivKey interface{}) (bool, error) {
	return b.inner.NotifyLiquidityManagementRequest(pubkey, nodePrivKey)
}

func (b *DashboardNotificationBridge) NotifyOnionMessageIncoming(pubkey string, nodePrivKey interface{}) (bool, error) {
	return b.inner.NotifyOnionMessageIncoming(pubkey, nodePrivKey)
}
