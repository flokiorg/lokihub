package lsps5

import (
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/lsps/events"
)

type WebhookRegisteredEvent struct {
	RequestID          string
	CounterpartyNodeID string
	AppName            string
	WebhookURL         string
	NumWebhooks        uint32
	MaxWebhooks        uint32
	NoChange           bool
}

func (e *WebhookRegisteredEvent) EventType() string {
	return constants.LSPS5_EVENT_WEBHOOK_REGISTERED
}

type WebhookRegistrationFailedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	AppName            string
	WebhookURL         string
	Error              string
}

func (e *WebhookRegistrationFailedEvent) EventType() string {
	return constants.LSPS5_EVENT_WEBHOOK_REGISTRATION_FAILED
}

type WebhooksListedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	AppNames           []string
	MaxWebhooks        uint32
}

func (e *WebhooksListedEvent) EventType() string {
	return constants.LSPS5_EVENT_WEBHOOKS_LISTED
}

type WebhookRemovedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	AppName            string
}

func (e *WebhookRemovedEvent) EventType() string {
	return constants.LSPS5_EVENT_WEBHOOK_REMOVED
}

type WebhookRemovalFailedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	AppName            string
	Error              string
}

func (e *WebhookRemovalFailedEvent) EventType() string {
	return constants.LSPS5_EVENT_WEBHOOK_REMOVAL_FAILED
}

// Ensure events implement Event interface
var _ events.Event = (*WebhookRegisteredEvent)(nil)
var _ events.Event = (*WebhookRegistrationFailedEvent)(nil)
var _ events.Event = (*WebhooksListedEvent)(nil)
var _ events.Event = (*WebhookRemovedEvent)(nil)
var _ events.Event = (*WebhookRemovalFailedEvent)(nil)
