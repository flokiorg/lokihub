package decodepay

import (
	"errors"
	"fmt"
	"math"
	"strings"

	fl "github.com/flokiorg/flndecodepay"
	"github.com/flokiorg/go-flokicoin/chainutil/bech32"
)

// ErrRepeatedPaymentHash refuses an invoice with more than one 32-byte payment
// hash field: readers disagree on which one counts, so a payer could pay a hash
// other than the one a claim proof binds.
var ErrRepeatedPaymentHash = errors.New("invoice has more than one payment hash")

const (
	bolt11TimestampWords = 7
	bolt11SignatureWords = 104
	bolt11FieldP         = 1
	bolt11HashWords      = 52
)

// paymentHashFields counts the tagged p fields of hash length in bolt11; a field
// of another length is skipped, as BOLT 11 says. 0 when bolt11 does not parse,
// which the full decode then reports.
func paymentHashFields(bolt11 string) int {
	_, data, err := bech32.DecodeNoLimit(bolt11)
	if err != nil || len(data) < bolt11TimestampWords+bolt11SignatureWords {
		return 0
	}
	fields := data[bolt11TimestampWords : len(data)-bolt11SignatureWords]
	n := 0
	for i := 0; i+3 <= len(fields); {
		length := int(fields[i+1])*32 + int(fields[i+2])
		if fields[i] == bolt11FieldP && length == bolt11HashWords {
			n++
		}
		i += 3 + length
	}
	return n
}

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
	AmountMloki     int64
	Network         string
	Route           [][]Hop
	// MinFinalCLTVExpiry is the final hop's CLTV delta the invoice asks for.
	MinFinalCLTVExpiry int64
}

func Decode(bolt11 string) (*Bolt11, error) {
	bolt11 = strings.ToLower(bolt11)
	if !strings.HasPrefix(bolt11, "ln") {
		return nil, fmt.Errorf("invalid flokicoin invoice prefix: %s", bolt11)
	}

	if paymentHashFields(bolt11) > 1 {
		return nil, ErrRepeatedPaymentHash
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
		AmountMloki:     int64(inv.MLoki),
		Network:         "flokicoin",
		Route:           routes,

		MinFinalCLTVExpiry: int64(inv.MinFinalCLTVExpiry),
	}, nil
}
