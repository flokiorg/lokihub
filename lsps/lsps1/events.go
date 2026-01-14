package lsps1

import "github.com/flokiorg/lokihub/lsps/events"

const (
	EventTypeSupportedOptionsReady  = "lsps1_supported_options_ready"
	EventTypeSupportedOptionsFailed = "lsps1_supported_options_failed"
	EventTypeOrderCreated           = "lsps1_order_created"
	EventTypeOrderRequestFailed     = "lsps1_order_request_failed"
	EventTypeOrderStatus            = "lsps1_order_status"
)

type SupportedOptionsReadyEvent struct {
	RequestID          string
	CounterpartyNodeID string
	SupportedOptions   Options
}

func (e *SupportedOptionsReadyEvent) EventType() string {
	return EventTypeSupportedOptionsReady
}

type SupportedOptionsFailedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	Error              string
}

func (e *SupportedOptionsFailedEvent) EventType() string {
	return EventTypeSupportedOptionsFailed
}

type OrderCreatedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	OrderID            string
	Order              OrderParams
	Payment            PaymentInfo
	Channel            *ChannelInfo
}

func (e *OrderCreatedEvent) EventType() string {
	return EventTypeOrderCreated
}

type OrderRequestFailedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	Error              string
}

func (e *OrderRequestFailedEvent) EventType() string {
	return EventTypeOrderRequestFailed
}

type OrderStatusEvent struct {
	RequestID          string
	CounterpartyNodeID string
	OrderID            string
	Order              OrderParams
	Payment            PaymentInfo
	Channel            *ChannelInfo
}

func (e *OrderStatusEvent) EventType() string {
	return EventTypeOrderStatus
}

// Ensure events implement Event interface
var _ events.Event = (*SupportedOptionsReadyEvent)(nil)
var _ events.Event = (*SupportedOptionsFailedEvent)(nil)
var _ events.Event = (*OrderCreatedEvent)(nil)
var _ events.Event = (*OrderRequestFailedEvent)(nil)
var _ events.Event = (*OrderStatusEvent)(nil)
