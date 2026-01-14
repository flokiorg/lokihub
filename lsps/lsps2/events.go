// Package lsps2 defines events for LSPS2 client
package lsps2

// Event types
const (
	EventTypeOpeningParametersReady = "lsps2_opening_parameters_ready"
	EventTypeGetInfoFailed          = "lsps2_get_info_failed"
	EventTypeInvoiceParametersReady = "lsps2_invoice_parameters_ready"
	EventTypeBuyRequestFailed       = "lsps2_buy_request_failed"
)

// OpeningParametersReadyEvent is emitted when LSP provides opening fee params
type OpeningParametersReadyEvent struct {
	RequestID            string
	CounterpartyNodeID   string
	OpeningFeeParamsMenu []OpeningFeeParams
}

func (e *OpeningParametersReadyEvent) EventType() string {
	return EventTypeOpeningParametersReady
}

// GetInfoFailedEvent is emitted when get_info request fails
type GetInfoFailedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	Error              string
}

func (e *GetInfoFailedEvent) EventType() string {
	return EventTypeGetInfoFailed
}

// InvoiceParametersReadyEvent is emitted when LSP provides invoice parameters
type InvoiceParametersReadyEvent struct {
	RequestID          string
	CounterpartyNodeID string
	InterceptSCID      uint64
	CLTVExpiryDelta    uint16
	PaymentSizeMloki   *uint64
}

func (e *InvoiceParametersReadyEvent) EventType() string {
	return EventTypeInvoiceParametersReady
}

// BuyRequestFailedEvent is emitted when buy request fails
type BuyRequestFailedEvent struct {
	RequestID          string
	CounterpartyNodeID string
	Error              string
}

func (e *BuyRequestFailedEvent) EventType() string {
	return EventTypeBuyRequestFailed
}
