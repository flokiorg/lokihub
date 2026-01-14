// Package lsps2 implements LSPS2 (JIT Channels) messages and types
package lsps2

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	MethodGetInfo = "lsps2.get_info"
	MethodBuy     = "lsps2.buy"
)

// GetInfoRequest requests JIT channel parameters from LSP
type GetInfoRequest struct {
	Token *string `json:"token,omitempty"`
}

// OpeningFeeParams contains fee parameters for opening a JIT channel
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

// RawOpeningFeeParams contains fee parameters before promise calculation
type RawOpeningFeeParams struct {
	MinFeeMloki          uint64
	Proportional         uint32
	ValidUntil           time.Time
	MinLifetime          uint32
	MaxClientToSelfDelay uint32
	MinPaymentSizeMloki  uint64
	MaxPaymentSizeMloki  uint64
}

// IntoOpeningFeeParams converts raw params to params with HMAC promise
func (p *RawOpeningFeeParams) IntoOpeningFeeParams(promiseSecret []byte, counterpartyNodeID string) OpeningFeeParams {
	h := hmac.New(sha256.New, promiseSecret)
	h.Write([]byte(counterpartyNodeID))

	// Write all fields to HMAC in Big Endian order (to match Rust to_be_bytes)

	// MinFeeMloki (u64)
	minFeeBytes := make([]byte, 8)
	for i := 0; i < 8; i++ {
		minFeeBytes[7-i] = byte(p.MinFeeMloki >> (8 * i))
	}
	h.Write(minFeeBytes)

	// Proportional (u32)
	propBytes := make([]byte, 4)
	for i := 0; i < 4; i++ {
		propBytes[3-i] = byte(p.Proportional >> (8 * i))
	}
	h.Write(propBytes)

	// ValidUntil (string)
	h.Write([]byte(p.ValidUntil.Format(time.RFC3339)))

	// MinLifetime (u32)
	lifetimeBytes := make([]byte, 4)
	for i := 0; i < 4; i++ {
		lifetimeBytes[3-i] = byte(p.MinLifetime >> (8 * i))
	}
	h.Write(lifetimeBytes)

	// MaxClientToSelfDelay (u32)
	delayBytes := make([]byte, 4)
	for i := 0; i < 4; i++ {
		delayBytes[3-i] = byte(p.MaxClientToSelfDelay >> (8 * i))
	}
	h.Write(delayBytes)

	// MinPaymentSizeMloki (u64)
	minPaymentBytes := make([]byte, 8)
	for i := 0; i < 8; i++ {
		minPaymentBytes[7-i] = byte(p.MinPaymentSizeMloki >> (8 * i))
	}
	h.Write(minPaymentBytes)

	// MaxPaymentSizeMloki (u64)
	maxPaymentBytes := make([]byte, 8)
	for i := 0; i < 8; i++ {
		maxPaymentBytes[7-i] = byte(p.MaxPaymentSizeMloki >> (8 * i))
	}
	h.Write(maxPaymentBytes)

	promise := hex.EncodeToString(h.Sum(nil))

	return OpeningFeeParams{
		MinFeeMloki:          p.MinFeeMloki,
		Proportional:         p.Proportional,
		ValidUntil:           p.ValidUntil,
		MinLifetime:          p.MinLifetime,
		MaxClientToSelfDelay: p.MaxClientToSelfDelay,
		MinPaymentSizeMloki:  p.MinPaymentSizeMloki,
		MaxPaymentSizeMloki:  p.MaxPaymentSizeMloki,
		Promise:              promise,
	}
}

// GetInfoResponse contains the LSP's JIT channel parameters
type GetInfoResponse struct {
	OpeningFeeParamsMenu []OpeningFeeParams `json:"opening_fee_params_menu"`
}

// BuyRequest requests a JIT channel with specific parameters
type BuyRequest struct {
	OpeningFeeParams OpeningFeeParams `json:"opening_fee_params"`
	PaymentSizeMloki *uint64          `json:"payment_size_mloki,string,omitempty"`
	PaymentSizeMsat  *uint64          `json:"payment_size_msat,string,omitempty"`
}

// BuyResponse contains the intercept SCID for the JIT channel
type BuyResponse struct {
	JitChannelSCID     string `json:"jit_channel_scid"`
	LSPCLTVExpiryDelta uint16 `json:"lsp_cltv_expiry_delta"`
	ClientTrustedLSP   bool   `json:"client_trusts_lsp"`
}

// ParseSCID converts the string SCID to uint64
func (r *BuyResponse) ParseSCID() (uint64, error) {
	// SCID format is "123x456x789"
	var blockHeight, txIndex, outputIndex uint64
	_, err := fmt.Sscanf(r.JitChannelSCID, "%dx%dx%d", &blockHeight, &txIndex, &outputIndex)
	if err != nil {
		return 0, err
	}

	// Encode as single uint64: (block_height << 40) | (tx_index << 16) | output_index
	scid := (blockHeight << 40) | (txIndex << 16) | outputIndex
	return scid, nil
}
