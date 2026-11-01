package lsps

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/lspd/common"
	"github.com/flokiorg/lokihub/lspd/core"
	"github.com/flokiorg/lokihub/lspd/platform/logging"
	lsps5 "github.com/flowgate-lsp/nostr-lsps5"
	"github.com/rs/zerolog/log"
)

// LSPS5 error codes per bLIP-55
const (
	Lsps5ErrorTooLong             = 500
	Lsps5ErrorUrlParseError       = 501
	Lsps5ErrorUnsupportedProtocol = 502
	Lsps5ErrorTooManyWebhooks     = 503
	Lsps5ErrorAppNameNotFound     = 1010
)

// bLIP-55 length limits
const (
	Lsps5MaxAppNameBytes = 64
	Lsps5MaxWebhookChars = 1024
)

type Lsps5SetWebhookRequest struct {
	AppName   string `json:"app_name"`
	Webhook   string `json:"webhook"`
	Transport string `json:"transport,omitempty"` // "push" or "nostr"
}

type Lsps5SetWebhookResponse struct {
	NumWebhooks int  `json:"num_webhooks"`
	MaxWebhooks int  `json:"max_webhooks"`
	NoChange    bool `json:"no_change"`
}

func (s *Handlers) handleLsps5SetWebhook(req *JsonRpcRequest, peer []byte) *JsonRpcResponse {
	peerId := hex.EncodeToString(peer)

	// Checked before anything else, as in lsps1.create_order and
	// lsps2.buy: set_webhook does DNS resolution, a DB write and an
	// outbound webhook_registered delivery, so a burst from one peer must
	// not reach any of that.
	if !s.burstLimiter.Allow(peerId) {
		return NewJsonRpcErrorResponse(req.Id, -32603, "Rate limit exceeded, please retry later", nil)
	}

	var params Lsps5SetWebhookRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		log.Trace().
			Err(err).
			Str("component", string(logging.LogComponentLSPS5)).
			Str("action", string(logging.LogActionValidate)).
			Msg("LSPS5: set_webhook invalid params")
		return NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params", nil)
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS5)).
		Str("action", string(logging.LogActionRequest)).
		Interface("params", params).
		Msg("LSPS5: Received set_webhook request")

	if params.AppName == "" || params.Webhook == "" {
		return NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params: app_name and webhook required", nil)
	}

	// bLIP-55: app_name must be <= 64 bytes UTF-8
	if len(params.AppName) > Lsps5MaxAppNameBytes {
		log.Warn().
			Str("component", string(logging.LogComponentLSPS5)).
			Str("action", string(logging.LogActionReject)).
			Int("app_name_length", len(params.AppName)).
			Msg("LSPS5: set_webhook rejected - app_name too long")
		return NewJsonRpcErrorResponse(req.Id, Lsps5ErrorTooLong, "too_long: app_name exceeds 64 bytes", nil)
	}

	// bLIP-55: webhook must be <= 1024 ASCII characters
	if len(params.Webhook) > Lsps5MaxWebhookChars {
		log.Warn().
			Str("component", string(logging.LogComponentLSPS5)).
			Str("action", string(logging.LogActionReject)).
			Int("webhook_length", len(params.Webhook)).
			Msg("LSPS5: set_webhook rejected - webhook too long")
		return NewJsonRpcErrorResponse(req.Id, Lsps5ErrorTooLong, "too_long: webhook exceeds 1024 characters", nil)
	}

	transport := params.Transport
	if transport == "" {
		transport = "push"
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS5)).
		Str("action", string(logging.LogActionValidate)).
		Str("transport", transport).
		Msg("LSPS5: set_webhook transport selection")

	if transport == "push" {
		// Check if HTTPS transport is enabled
		if !s.config.EnableLsps5Https {
			return NewJsonRpcErrorResponse(req.Id, Lsps5ErrorUnsupportedProtocol, "https_transport_disabled", nil)
		}
		if err := common.ValidateWebhookUrl(params.Webhook, s.config.Lsps5AllowLoopbackWebhooks); err != nil {
			log.Warn().
				Err(err).
				Str("component", string(logging.LogComponentLSPS5)).
				Str("action", string(logging.LogActionReject)).
				Str("url", params.Webhook).
				Msg("LSPS5: set_webhook rejected - invalid URL")

			errorCode := Lsps5ErrorUrlParseError
			if strings.Contains(err.Error(), "unsupported_protocol") {
				errorCode = Lsps5ErrorUnsupportedProtocol
			}
			return NewJsonRpcErrorResponse(req.Id, errorCode, fmt.Sprintf("invalid_webhook_url: %v", err), nil)
		}
	} else if transport == "nostr" {
		// Check if Nostr transport is enabled
		if !s.config.EnableLsps5Nostr {
			return NewJsonRpcErrorResponse(req.Id, Lsps5ErrorUnsupportedProtocol, "nostr_transport_disabled", nil)
		}
		// Use library validation for URI
		if _, _, err := lsps5.ParseLsp5Uri(params.Webhook); err != nil {
			log.Debug().
				Err(err).
				Str("component", string(logging.LogComponentLSPS5)).
				Str("action", string(logging.LogActionValidate)).
				Str("uri", params.Webhook).
				Msg("LSPS5: set_webhook invalid nostr URI")
			return NewJsonRpcErrorResponse(req.Id, Lsps5ErrorUrlParseError, fmt.Sprintf("invalid_nostr_uri: %v", err), nil)
		}
	} else {
		log.Warn().
			Str("component", string(logging.LogComponentLSPS5)).
			Str("action", string(logging.LogActionReject)).
			Str("transport", transport).
			Msg("LSPS5: set_webhook unsupported transport")
		return NewJsonRpcErrorResponse(req.Id, -32602, "unsupported_transport", nil)
	}

	existingWebhooks, err := s.openingService.GetLsps5Webhooks(peerId)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentLSPS5)).
			Str("action", string(logging.LogActionFailure)).
			Msg("LSPS5: Failed to fetch existing webhooks")
		return NewJsonRpcErrorResponse(req.Id, -32603, "Internal error", nil)
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS5)).
		Int("existing_count", len(existingWebhooks)).
		Msg("LSPS5: set_webhook fetched existing webhooks")

	noChange := false
	found := false
	for _, w := range existingWebhooks {
		if w.AppName == params.AppName {
			found = true
			if w.Url == params.Webhook && w.Transport == transport {
				noChange = true
			}
			break
		}
	}

	if !found && len(existingWebhooks) >= s.config.Lsps5MaxWebhooks {
		return NewJsonRpcErrorResponse(req.Id, Lsps5ErrorTooManyWebhooks, "too_many_webhooks", map[string]int{"max_webhooks": s.config.Lsps5MaxWebhooks})
	}

	webhook := &core.Lsps5Webhook{
		ClientNodeId: peerId,
		AppName:      params.AppName,
		Url:          params.Webhook,
		Transport:    transport,
		CreatedAt:    time.Now(),
	}

	log.Trace().
		Str("component", string(logging.LogComponentLSPS5)).
		Str("action", "registering").
		Str("transport", transport).
		Str("url", params.Webhook).
		Bool("no_change", noChange).
		Bool("found", found).
		Msg("LSPS5: set_webhook proceeding to register")

	if !noChange {
		err = s.openingService.RegisterLsps5Webhook(webhook)
		if err != nil {
			log.Error().
				Err(err).
				Str("component", string(logging.LogComponentLSPS5)).
				Str("action", string(logging.LogActionFailure)).
				Msg("LSPS5: Failed to persist webhook in database")
			return NewJsonRpcErrorResponse(req.Id, -32603, "Internal error", nil)
		}

		// bLIP-55: Send lsps5.webhook_registered notification to newly registered webhook
		if s.notificationService != nil {
			go s.notificationService.NotifyWebhookRegistered(peerId, webhook)
		}

		log.Info().
			Str("component", string(logging.LogComponentLSPS5)).
			Str("peer_id", peerId).
			Str("app_name", params.AppName).
			Str("transport", transport).
			Msg("LSPS5: webhook registered")
	}

	numWebhooks := len(existingWebhooks)
	if !found {
		numWebhooks++
	}

	resp := Lsps5SetWebhookResponse{
		NumWebhooks: numWebhooks,
		MaxWebhooks: s.config.Lsps5MaxWebhooks,
		NoChange:    noChange,
	}

	return NewJsonRpcResponse(req.Id, resp)
}

type Lsps5ListWebhooksResponse struct {
	AppNames    []string `json:"app_names"`
	MaxWebhooks int      `json:"max_webhooks"`
}

func (s *Handlers) handleLsps5ListWebhooks(req *JsonRpcRequest, peer []byte) *JsonRpcResponse {
	peerId := hex.EncodeToString(peer)
	log.Trace().
		Str("component", string(logging.LogComponentLSPS5)).
		Str("action", string(logging.LogActionRequest)).
		Str("peer_id", peerId).
		Msg("LSPS5: Received list_webhooks request")
	webhooks, err := s.openingService.GetLsps5Webhooks(peerId)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentLSPS5)).
			Str("action", string(logging.LogActionFailure)).
			Str("peer_id", peerId).
			Msg("LSPS5: Failed to list webhooks from database")
		return NewJsonRpcErrorResponse(req.Id, -32603, "Internal error", nil)
	}

	appNames := []string{}
	for _, w := range webhooks {
		appNames = append(appNames, w.AppName)
	}

	resp := Lsps5ListWebhooksResponse{
		AppNames:    appNames,
		MaxWebhooks: s.config.Lsps5MaxWebhooks,
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS5)).
		Str("action", string(logging.LogActionResponse)).
		Msgf("LSPS5: list_webhooks success, count=%d", len(appNames))
	return NewJsonRpcResponse(req.Id, resp)
}

type Lsps5RemoveWebhookRequest struct {
	AppName string `json:"app_name"`
}

func (s *Handlers) handleLsps5RemoveWebhook(req *JsonRpcRequest, peer []byte) *JsonRpcResponse {
	var params Lsps5RemoveWebhookRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		log.Trace().
			Err(err).
			Str("component", string(logging.LogComponentLSPS5)).
			Str("action", string(logging.LogActionValidate)).
			Msg("LSPS5: remove_webhook invalid params")
		return NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params", nil)
	}
	log.Trace().
		Str("component", string(logging.LogComponentLSPS5)).
		Str("action", string(logging.LogActionRequest)).
		Interface("params", params).
		Msg("LSPS5: Received remove_webhook request")

	if params.AppName == "" {
		return NewJsonRpcErrorResponse(req.Id, -32602, "Invalid params: app_name required", nil)
	}

	peerId := hex.EncodeToString(peer)
	err := s.openingService.RemoveLsps5Webhook(peerId, params.AppName)
	if err != nil {
		log.Error().
			Err(err).
			Str("component", string(logging.LogComponentLSPS5)).
			Str("action", string(logging.LogActionFailure)).
			Msg("LSPS5: Failed to remove webhook from database")
		// bLIP-55: Return app_name_not_found if not found (we could check error type)
		return NewJsonRpcErrorResponse(req.Id, Lsps5ErrorAppNameNotFound, "app_name_not_found", nil)
	}

	log.Trace().
		Str("component", string(logging.LogComponentLSPS5)).
		Str("action", string(logging.LogActionResponse)).
		Msg("LSPS5: remove_webhook success")
	return NewJsonRpcResponse(req.Id, map[string]interface{}{})
}
