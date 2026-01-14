package decodepay

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flokiorg/flnd/zpay32"
	"github.com/flokiorg/go-flokicoin/chaincfg"
	_ "github.com/flokiorg/go-flokicoin/crypto"
)

func Decodepay(bolt11 string) (Bolt11, error) {
	if len(bolt11) < 2 {
		return Bolt11{}, errors.New("bolt11 too short")
	}

	firstNumber := strings.IndexAny(bolt11, "1234567890")
	if firstNumber < 2 {
		return Bolt11{}, errors.New("invalid bolt11 invoice")
	}

	chainPrefix := strings.ToLower(bolt11[2:firstNumber])
	chain := &chaincfg.Params{
		Bech32HRPSegwit: chainPrefix,
	}

	inv, err := zpay32.Decode(bolt11, chain)
	if err != nil {
		return Bolt11{}, fmt.Errorf("zpay32 decoding failed: %w", err)
	}

	var mloki int64
	if inv.MilliSat != nil {
		mloki = int64(*inv.MilliSat)
	}

	var desc string
	if inv.Description != nil {
		desc = *inv.Description
	}

	var deschash string
	if inv.DescriptionHash != nil {
		dh := *inv.DescriptionHash
		deschash = hex.EncodeToString(dh[:])
	}

	var routes [][]Hop
	if inv.RouteHints != nil {
		routes = make([][]Hop, len(inv.RouteHints))
		for ri, r := range inv.RouteHints {
			route := make([]Hop, len(r))
			for hi, h := range r {
				scid := h.ChannelID

				route[hi] = Hop{
					PubKey: hex.EncodeToString(h.NodeID.SerializeCompressed()),
					ShortChannelId: fmt.Sprintf("%dx%dx%d",
						scid>>40&0xFFFFFF, scid>>16&0xFFFFFF, scid&0xFFFF),
					FeeBaseMloki:              int(h.FeeBaseMSat),
					FeeProportionalMillionths: int(h.FeeProportionalMillionths),
					CLTVExpiryDelta:           int(h.CLTVExpiryDelta),
				}
			}
			routes[ri] = route
		}
	}

	return Bolt11{
		MLoki:              mloki,
		PaymentHash:        hex.EncodeToString(inv.PaymentHash[:]),
		Description:        desc,
		DescriptionHash:    deschash,
		Payee:              hex.EncodeToString(inv.Destination.SerializeCompressed()),
		CreatedAt:          int(inv.Timestamp.Unix()),
		Expiry:             int(inv.Expiry() / time.Second),
		MinFinalCLTVExpiry: int(inv.MinFinalCLTVExpiry()),
		Currency:           inv.Net.Bech32HRPSegwit,
		Route:              routes,
	}, nil
}

type Bolt11 struct {
	Currency           string  `json:"currency"`
	CreatedAt          int     `json:"created_at"`
	Expiry             int     `json:"expiry"`
	Payee              string  `json:"payee"`
	MLoki              int64   `json:"mloki"`
	Description        string  `json:"description,omitempty"`
	DescriptionHash    string  `json:"description_hash,omitempty"`
	PaymentHash        string  `json:"payment_hash"`
	MinFinalCLTVExpiry int     `json:"min_final_cltv_expiry"`
	Route              [][]Hop `json:"routes,omitempty"`
}

type Hop struct {
	PubKey                    string `json:"pubkey"`
	ShortChannelId            string `json:"short_channel_id"`
	FeeBaseMloki              int    `json:"fee_base_mloki"`
	FeeProportionalMillionths int    `json:"fee_proportional_millionths"`
	CLTVExpiryDelta           int    `json:"cltv_expiry_delta"`
}
