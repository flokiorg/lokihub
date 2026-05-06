package decodepay

import (
	"fmt"
	"strings"

	fl "github.com/flokiorg/flndecodepay"
)

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
	if !strings.HasPrefix(bolt11, "ln") && !strings.HasPrefix(bolt11, "fln") {
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
				FeeBaseMloki:              uint64(h.FeeBaseMloki),
				FeeProportionalMillionths: uint64(h.FeeProportionalMillionths),
				CltvExpiryDelta:           uint32(h.CLTVExpiryDelta),
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
