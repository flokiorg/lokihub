package lsps1

import (
	"time"
)

const (
	MethodGetInfo     = "lsps1.get_info"
	MethodCreateOrder = "lsps1.create_order"
	MethodGetOrder    = "lsps1.get_order"
)

// GetInfoRequest requests supported options from LSP
type GetInfoRequest struct{}

// OpeningFeeParams contains fee parameters for opening a channel
type OpeningFeeParams struct {
	MinFeeMloki          uint64    `json:"min_fee_mloki,string"`
	Proportional         uint32    `json:"proportional"`
	ValidUntil           time.Time `json:"valid_until"`
	MinLifetime          uint32    `json:"min_lifetime"`
	MaxClientToSelfDelay uint32    `json:"max_client_to_self_delay"`
	MinPaymentSizeMloki  uint64    `json:"min_payment_size_mloki,string"`
	MaxPaymentSizeMloki  uint64    `json:"max_payment_size_mloki,string"`
	Promise              string    `json:"promise"`
}

// Options represents supported protocol options
type Options struct {
	MinRequiredChannelConfirmations uint16             `json:"min_required_channel_confirmations"`
	MinFundingConfirmsWithinBlocks  uint16             `json:"min_funding_confirms_within_blocks"`
	SupportsZeroChannelReserve      bool               `json:"supports_zero_channel_reserve"`
	MaxChannelExpiryBlocks          uint32             `json:"max_channel_expiry_blocks"`
	MinInitialClientBalanceLoki     uint64             `json:"min_initial_client_balance_loki,string"`
	MaxInitialClientBalanceLoki     uint64             `json:"max_initial_client_balance_loki,string"`
	MinInitialLspBalanceLoki        uint64             `json:"min_initial_lsp_balance_loki,string"`
	MaxInitialLspBalanceLoki        uint64             `json:"max_initial_lsp_balance_loki,string"`
	MinChannelBalanceLoki           uint64             `json:"min_channel_balance_loki,string"`
	MaxChannelBalanceLoki           uint64             `json:"max_channel_balance_loki,string"`
	OpeningFeeParams                []OpeningFeeParams `json:"opening_fee_params"`
}

// GetInfoResponse contains supported options
type GetInfoResponse struct {
	Options
}

// CreateOrderRequest requests to create a channel order
type CreateOrderRequest struct {
	OrderParams
	RefundOnchainAddress *string `json:"refund_onchain_address,omitempty"`
}

// OrderParams represents channel order parameters
type OrderParams struct {
	LspBalanceLoki               uint64            `json:"lsp_balance_loki,string"`
	ClientBalanceLoki            uint64            `json:"client_balance_loki,string"`
	RequiredChannelConfirmations uint16            `json:"required_channel_confirmations"`
	FundingConfirmsWithinBlocks  uint16            `json:"funding_confirms_within_blocks"`
	ChannelExpiryBlocks          uint32            `json:"channel_expiry_blocks"`
	Token                        *string           `json:"token,omitempty"`
	AnnounceChannel              bool              `json:"announce_channel"`
	OpeningFeeParams             *OpeningFeeParams `json:"opening_fee_params,omitempty"`
}

// CreateOrderResponse contains order details and payment info
type CreateOrderResponse struct {
	OrderID string `json:"order_id"`
	OrderParams
	CreatedAt  time.Time    `json:"created_at"`
	OrderState string       `json:"order_state"`
	Payment    PaymentInfo  `json:"payment"`
	Channel    *ChannelInfo `json:"channel,omitempty"`
}

type PaymentInfo struct {
	Bolt11  *Bolt11PaymentInfo  `json:"bolt11,omitempty"`
	Bolt12  *Bolt12PaymentInfo  `json:"bolt12,omitempty"`
	Onchain *OnchainPaymentInfo `json:"onchain,omitempty"`
}

type Bolt11PaymentInfo struct {
	State          string    `json:"state"`
	ExpiresAt      time.Time `json:"expires_at"`
	FeeTotalLoki   uint64    `json:"fee_total_loki,string"`
	OrderTotalLoki uint64    `json:"order_total_loki,string"`
	Invoice        string    `json:"invoice"`
}

type Bolt12PaymentInfo struct {
	State          string    `json:"state"`
	ExpiresAt      time.Time `json:"expires_at"`
	FeeTotalLoki   uint64    `json:"fee_total_loki,string"`
	OrderTotalLoki uint64    `json:"order_total_loki,string"`
	Offer          string    `json:"offer"`
}

type OnchainPaymentInfo struct {
	State                          string    `json:"state"`
	ExpiresAt                      time.Time `json:"expires_at"`
	FeeTotalLoki                   uint64    `json:"fee_total_loki,string"`
	OrderTotalLoki                 uint64    `json:"order_total_loki,string"`
	Address                        string    `json:"address"`
	MinOnchainPaymentConfirmations *uint16   `json:"min_onchain_payment_confirmations,omitempty"`
	MinFeeFor0Conf                 uint32    `json:"min_fee_for_0conf"`
	RefundOnchainAddress           *string   `json:"refund_onchain_address,omitempty"`
}

type ChannelInfo struct {
	FundedAt        time.Time `json:"funded_at"`
	FundingOutpoint string    `json:"funding_outpoint"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// GetOrderRequest requests details of an existing order
type GetOrderRequest struct {
	OrderID string `json:"order_id"`
}

// JsonRpcRequest/Response from LSPS0 are used for wrapping these
