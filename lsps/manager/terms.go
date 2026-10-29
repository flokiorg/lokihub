package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"strings"

	"github.com/ohstr/nmilat/niplsp"

	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/persist"
)

// ErrTermsWorse refuses a manual purchase whose live terms are worse for the
// buyer than the LSP announced, until the user has seen and accepted them
// (NIP-LSP §Client Behavior, Live check).
var ErrTermsWorse = errors.New("the LSP's live terms are worse than it announced")

// TermField is one compared value: its announced and live values and the
// verdict for the buyer (same, worse or better).
type TermField struct {
	Field     string `json:"field"`
	Announced string `json:"announced"`
	Live      string `json:"live"`
	Verdict   string `json:"verdict"`
}

// Terms are live LSPS terms compared with the LSP's announcement.
type Terms struct {
	Worse  bool        `json:"worse"`
	Fields []TermField `json:"fields"`
}

// AcceptedTerm is a live value worse than announced that the user saw and
// accepted.
type AcceptedTerm struct {
	Field string `json:"field"`
	Live  string `json:"live"`
}

// TermsWorseError is ErrTermsWorse with the terms compared, so the user can be
// shown both and asked again.
type TermsWorseError struct{ Terms Terms }

func (e *TermsWorseError) Error() string {
	var worse []string
	for _, f := range e.Terms.Fields {
		if f.Verdict == niplsp.Worse.String() {
			worse = append(worse, fmt.Sprintf("%s %s (announced %s)", f.Field, orNone(f.Live), orNone(f.Announced)))
		}
	}
	return fmt.Sprintf("%s: %s", ErrTermsWorse, strings.Join(worse, ", "))
}

func (e *TermsWorseError) Is(target error) bool { return target == ErrTermsWorse }

// TermsRefusal is how both transports answer a purchase refused for terms
// worse than announced: the message and both sets of terms, so the user can be
// asked again (HTTP 409 body; the desktop app's error body).
type TermsRefusal struct {
	Message string `json:"message"`
	Terms   Terms  `json:"terms"`
}

// TermsRefusalOf is err's TermsRefusal when err is a *TermsWorseError.
func TermsRefusalOf(err error) (*TermsRefusal, bool) {
	var tw *TermsWorseError
	if !errors.As(err, &tw) {
		return nil, false
	}
	return &TermsRefusal{Message: err.Error(), Terms: tw.Terms}, true
}

func orNone(v string) string {
	if v == "" {
		return "none"
	}
	return v
}

func termsOf(fields []niplsp.FieldDiff) Terms {
	t := Terms{Fields: []TermField{}}
	for _, f := range fields {
		t.Fields = append(t.Fields, TermField{Field: f.Field, Announced: f.Announced, Live: f.Live, Verdict: f.Verdict.String()})
		if f.Verdict == niplsp.Worse {
			t.Worse = true
		}
	}
	return t
}

// acceptedBy reports that every worse value was accepted at exactly its live
// value.
func (t Terms) acceptedBy(accepted []AcceptedTerm) bool {
	ok := map[AcceptedTerm]bool{}
	for _, a := range accepted {
		ok[a] = true
	}
	for _, f := range t.Fields {
		if f.Verdict == niplsp.Worse.String() && !ok[AcceptedTerm{Field: f.Field, Live: f.Live}] {
			return false
		}
	}
	return true
}

// announced is the catalogue row's announced terms, as discovery stored them
// from the newest usable announcement.
func announced(row *persist.LSP) (*niplsp.Announcement, error) {
	var a niplsp.Announcement
	for _, f := range []struct {
		raw string
		v   any
	}{{row.Protocols, &a.Protocols}, {row.LSPS1, &a.LSPS1}, {row.LSPS2Fees, &a.LSPS2}} {
		if f.raw == "" || f.raw == "null" {
			continue
		}
		if err := json.Unmarshal([]byte(f.raw), f.v); err != nil {
			return nil, fmt.Errorf("%w: its stored announcement cannot be read", ErrLSPNotUsable)
		}
	}
	return &a, nil
}

// feeRows maps lokihub's LSPS2 menu (_mloki) to announced fee rows.
func feeRows(menu []lsps2.OpeningFeeParams) ([]niplsp.FeeRow, error) {
	raw, err := json.Marshal(map[string]any{"opening_fee_params_menu": menu})
	if err != nil {
		return nil, err
	}
	return niplsp.ParseLSPS2Menu(raw)
}

// lsps2Announced is the announcement of an LSP that announces LSPS2 with at
// least one fee row; nothing is bought from one that announces none.
func lsps2Announced(row *persist.LSP) (*niplsp.Announcement, error) {
	a, err := announced(row)
	if err != nil {
		return nil, err
	}
	if !a.HasProtocol(niplsp.ProtocolLSPS2) || len(a.LSPS2) == 0 {
		return nil, fmt.Errorf("%w: it announces no LSPS2 fee terms to compare with", ErrLSPNotUsable)
	}
	return a, nil
}

// compareFeeRow compares the fee row offered first in the live menu with the
// first announced row, the one it stands for.
func compareFeeRow(row *persist.LSP, p lsps2.OpeningFeeParams) (Terms, error) {
	a, err := lsps2Announced(row)
	if err != nil {
		return Terms{}, err
	}
	live, err := feeRows([]lsps2.OpeningFeeParams{p})
	if err != nil {
		return Terms{}, fmt.Errorf("the LSP's fee row cannot be read: %w", err)
	}
	first := &niplsp.Announcement{Fields: niplsp.Fields{LSPS2: a.LSPS2[:1]}}
	return termsOf(niplsp.CheckLive(first, niplsp.Live{LSPS2Menu: live}).Fields), nil
}

// compareFeeMenu compares a whole live menu with the announced rows.
func compareFeeMenu(row *persist.LSP, menu []lsps2.OpeningFeeParams) (Terms, error) {
	a, err := lsps2Announced(row)
	if err != nil {
		return Terms{}, err
	}
	live, err := feeRows(menu)
	if err != nil {
		return Terms{}, fmt.Errorf("the LSP's fee menu cannot be read: %w", err)
	}
	return termsOf(niplsp.CheckLive(a, niplsp.Live{LSPS2Menu: live}).Fields), nil
}

// compareLSPS1 compares live LSPS1 options with the announced ones; LSPS1
// announces bounds only, no fee.
func compareLSPS1(row *persist.LSP, o lsps1.Options) (Terms, error) {
	a, err := announced(row)
	if err != nil {
		return Terms{}, err
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return Terms{}, err
	}
	live, err := niplsp.ParseLSPS1Info(raw)
	if err != nil {
		return Terms{}, fmt.Errorf("the LSP's LSPS1 options cannot be read: %w", err)
	}
	return termsOf(niplsp.CheckLive(&niplsp.Announcement{Fields: niplsp.Fields{LSPS1: a.LSPS1}}, niplsp.Live{LSPS1: live}).Fields), nil
}

// lsps1Only keeps a live check's LSPS1 values.
func lsps1Only(fields []niplsp.FieldDiff) []niplsp.FieldDiff {
	var out []niplsp.FieldDiff
	for _, f := range fields {
		if !strings.HasPrefix(f.Field, "lsps2_fee[") {
			out = append(out, f)
		}
	}
	return out
}

// LSPS2Terms compares the fee row the user is offered with the announced one.
func (m *LiquidityManager) LSPS2Terms(pubkey string, p lsps2.OpeningFeeParams) (*Terms, error) {
	row, err := m.contactable(pubkey)
	if err != nil {
		return nil, err
	}
	t, err := compareFeeRow(row, p)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// LSPS1Terms compares live LSPS1 options with the announced ones.
func (m *LiquidityManager) LSPS1Terms(pubkey string, o lsps1.Options) (*Terms, error) {
	row, err := m.contactable(pubkey)
	if err != nil {
		return nil, err
	}
	t, err := compareLSPS1(row, o)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// jitFee is the opening fee of a JIT payment of amountMloki under p: the
// larger of min_fee and the proportional fee, rounded up. It refuses a payment
// outside p's size bounds and a fee that would take the whole payment.
func jitFee(amountMloki uint64, p lsps2.OpeningFeeParams) (uint64, error) {
	if amountMloki < p.MinPaymentSizeMloki || (p.MaxPaymentSizeMloki > 0 && amountMloki > p.MaxPaymentSizeMloki) {
		return 0, fmt.Errorf("the payment of %d mloki is outside the LSP's bounds %d..%d", amountMloki, p.MinPaymentSizeMloki, p.MaxPaymentSizeMloki)
	}
	hi, lo := bits.Mul64(amountMloki, uint64(p.Proportional))
	lo, carry := bits.Add64(lo, 999_999, 0)
	hi += carry
	if hi >= 1_000_000 {
		return 0, fmt.Errorf("the LSP's proportional fee overflows")
	}
	proportional, _ := bits.Div64(hi, lo, 1_000_000)
	fee := max(p.MinFeeMloki, proportional)
	if fee >= amountMloki {
		return 0, fmt.Errorf("the LSP's opening fee of %d mloki would take the whole payment of %d mloki", fee, amountMloki)
	}
	return fee, nil
}
