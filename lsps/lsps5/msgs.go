package lsps5

import (
	"encoding/json"
	"fmt"
)

const (
	MethodSetWebhook    = "lsps5.set_webhook"
	MethodListWebhooks  = "lsps5.list_webhooks"
	MethodRemoveWebhook = "lsps5.remove_webhook"

	MethodWebhookRegistered          = "lsps5.webhook_registered"
	MethodPaymentIncoming            = "lsps5.payment_incoming"
	MethodExpirySoon                 = "lsps5.expiry_soon"
	MethodLiquidityManagementRequest = "lsps5.liquidity_management_request"
	MethodOnionMessageIncoming       = "lsps5.onion_message_incoming"
)

// SetWebhookRequest parameters
type SetWebhookRequest struct {
	AppName string `json:"app_name"`
	Webhook string `json:"webhook"`
}

// SetWebhookResponse response
type SetWebhookResponse struct {
	NumWebhooks uint32 `json:"num_webhooks"`
	MaxWebhooks uint32 `json:"max_webhooks"`
	NoChange    bool   `json:"no_change"`
}

// ListWebhooksRequest parameters (empty)
type ListWebhooksRequest struct{}

// ListWebhooksResponse response
type ListWebhooksResponse struct {
	AppNames    []string `json:"app_names"`
	MaxWebhooks uint32   `json:"max_webhooks"`
}

// RemoveWebhookRequest parameters
type RemoveWebhookRequest struct {
	AppName string `json:"app_name"`
}

// RemoveWebhookResponse response (empty)
type RemoveWebhookResponse struct{}

// WebhookNotification represents notification payload
type WebhookNotification struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

// ExpirySoonParams parameters for lsps5.expiry_soon
type ExpirySoonParams struct {
	Timeout uint32 `json:"timeout"`
}

// Helper to decode notification params
func DecodeExpirySoonParams(params interface{}) (*ExpirySoonParams, error) {
	bytes, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var p ExpirySoonParams
	if err := json.Unmarshal(bytes, &p); err != nil {
		return nil, fmt.Errorf("invalid expiry_soon params: %w", err)
	}
	return &p, nil
}
