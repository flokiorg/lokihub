// LSPS5 Event Types - must match backend constants
export enum LSPS5EventType {
  Notification = "lsps5.notification",
  PaymentIncoming = "lsps5.payment_incoming",
  ExpirySoon = "lsps5.expiry_soon",
  LiquidityRequest = "lsps5.liquidity_request",
  OnionMessage = "lsps5.onion_message",
  OrderStateChanged = "lsps5.order_state_changed",
  WebhookRegistered = "lsps5.webhook_registered",
  WebhookRegistrationFailed = "lsps5.webhook_registration_failed",
  WebhooksListed = "lsps5.webhooks_listed",
  WebhookRemoved = "lsps5.webhook_removed",
  WebhookRemovalFailed = "lsps5.webhook_removal_failed",
}

// LSPS1 Event Types
export enum LSPS1EventType {
  Notification = "lsps1.notification",
}
