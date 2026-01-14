package lsps5

import "github.com/flokiorg/lokihub/lsps/events"

const (
	EventTypeWebhookRegistered         = "lsps5_webhook_registered"
	EventTypeWebhookRegistrationFailed = "lsps5_webhook_registration_failed"
	EventTypeWebhooksListed            = "lsps5_webhooks_listed"
	EventTypeWebhookRemoved            = "lsps5_webhook_removed"
	EventTypeWebhookRemovalFailed      = "lsps5_webhook_removal_failed"
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
	return EventTypeWebhookRegistered
}

type WebhookRegistrationFailedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	AppName            string
	WebhookURL         string
	Error              string
}

func (e *WebhookRegistrationFailedEvent) EventType() string {
	return EventTypeWebhookRegistrationFailed
}

type WebhooksListedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	AppNames           []string
	MaxWebhooks        uint32
}

func (e *WebhooksListedEvent) EventType() string {
	return EventTypeWebhooksListed
}

type WebhookRemovedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	AppName            string
}

func (e *WebhookRemovedEvent) EventType() string {
	return EventTypeWebhookRemoved
}

type WebhookRemovalFailedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	AppName            string
	Error              string
}

func (e *WebhookRemovalFailedEvent) EventType() string {
	return EventTypeWebhookRemovalFailed
}

// Ensure events implement Event interface
var _ events.Event = (*WebhookRegisteredEvent)(nil)
var _ events.Event = (*WebhookRegistrationFailedEvent)(nil)
var _ events.Event = (*WebhooksListedEvent)(nil)
var _ events.Event = (*WebhookRemovedEvent)(nil)
var _ events.Event = (*WebhookRemovalFailedEvent)(nil)
