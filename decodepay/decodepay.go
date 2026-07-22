package decodepay

import (
	"fmt"
	"math"
	"strings"

	fl "github.com/flokiorg/flndecodepay"
)

// clampIntToUint64 and clampIntToUint32 clamp route hint fields decoded from
// a bolt11 invoice - genuinely external, potentially adversarial data
// (anyone can hand you an invoice to pay) - rather than silently wrapping an
// out-of-range value into a misleadingly small one.
func clampIntToUint64(v int) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

func clampIntToUint32(v int) uint32 {
	if v < 0 {
		return 0
	}
	if v > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(v)
}

type Hop struct {
	PubKey                    string
	ChanId                    uint64
	FeeBaseMloki              uint64
	FeeProportionalMillionths uint64
	CltvExpiryDelta           uint32
}

type Bolt11 struct {
	PaymentHash     string
	Description     string
	DescriptionHash string
	Payee           string
	CreatedAt       int64
	Expiry          int64
	MSat            int64
	Network         string
	Route           [][]Hop
}

func Decode(bolt11 string) (*Bolt11, error) {
	bolt11 = strings.ToLower(bolt11)
	if !strings.HasPrefix(bolt11, "ln") {
		return nil, fmt.Errorf("invalid flokicoin invoice prefix: %s", bolt11)
	}

	// Flokicoin
	inv, err := fl.Decodepay(bolt11)
	if err != nil {
		return nil, err
	}

	var routes [][]Hop
	for _, r := range inv.Route {
		var hops []Hop
		for _, h := range r {
			hops = append(hops, Hop{
				PubKey:                    h.PubKey,
				ChanId:                    0, // flndecodepay has ShortChannelId as string
				FeeBaseMloki:              clampIntToUint64(h.FeeBaseMloki),
				FeeProportionalMillionths: clampIntToUint64(h.FeeProportionalMillionths),
				CltvExpiryDelta:           clampIntToUint32(h.CLTVExpiryDelta),
			})
		}
		routes = append(routes, hops)
	}

	return &Bolt11{
		PaymentHash:     inv.PaymentHash,
		Description:     inv.Description,
		DescriptionHash: inv.DescriptionHash,
		Payee:           inv.Payee,
		CreatedAt:       int64(inv.CreatedAt),
		Expiry:          int64(inv.Expiry),
		MSat:            int64(inv.MLoki),
		Network:         "flokicoin",
		Route:           routes,
	}, nil
}
