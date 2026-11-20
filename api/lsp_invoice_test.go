package api

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flokiorg/lokihub/internal/decodepay"
	"github.com/flokiorg/lokihub/tests"
)

// TestVerifyLSPInvoice (pentest C F3): the order invoice must charge exactly
// the quoted total and pay the LSP's own node.
func TestVerifyLSPInvoice(t *testing.T) {
	inv, _ := tests.SignedInvoice(21_000_000, time.Now(), time.Hour) // 21,000 loki
	d, err := decodepay.Decode(inv)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLSPInvoice(inv, 21_000, d.Payee); err != nil {
		t.Errorf("matching invoice refused: %v", err)
	}
	if err := verifyLSPInvoice(inv, 21_000, d.Payee+"@127.0.0.1:9735"); err != nil {
		t.Errorf("pubkey@host form refused: %v", err)
	}
	if err := verifyLSPInvoice(inv, 1_000, d.Payee); !errors.Is(err, ErrLSPInvoiceMismatch) {
		t.Errorf("over-billing invoice accepted: %v", err)
	}
	other := "02" + strings.Repeat("11", 32)
	if err := verifyLSPInvoice(inv, 21_000, other); !errors.Is(err, ErrLSPInvoiceMismatch) {
		t.Errorf("invoice paying another node accepted: %v", err)
	}
	if err := verifyLSPInvoice("lnbcrt1garbage", 21_000, d.Payee); !errors.Is(err, ErrLSPInvoiceMismatch) {
		t.Errorf("garbage invoice accepted: %v", err)
	}
}
