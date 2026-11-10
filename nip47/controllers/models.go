package controllers

import (
	"errors"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/transactions"
)

type publishFunc = func(*models.Response, nostr.Tags)

type payResponse struct {
	Preimage string `json:"preimage"`
	FeesPaid uint64 `json:"fees_paid"`
	// FeeSkimMloki is the circle-hub forwarding-fee cut (CircleHubConfig.FeesPpm)
	// debited from a circle_wallet's own payment, on top of FeesPaid (the real
	// Lightning routing fee) — omitted for every other app kind/payment, and
	// for a circle_wallet payment that never leaves this instance
	// (self-payment-exempt, see transactions_service.go's validateCanPay).
	// Without this, a circle member had no NWC-facing way to learn why their
	// balance dropped by more than invoice_amount+fees_paid: the value was
	// always correctly charged and recorded (db.Transaction.FeeSkimMloki),
	// just previously exposed only to the wallet owner via the admin API.
	FeeSkimMloki uint64 `json:"fee_skim_mloki,omitempty"`
}

// payResultOf is the NIP-47 result of a completed payment, or an error if the
// payment went through without yielding a preimage.
func payResultOf(tx *transactions.Transaction) (payResponse, error) {
	if tx == nil || tx.Preimage == nil {
		return payResponse{}, errors.New("payment completed but preimage unavailable")
	}
	return payResponse{Preimage: *tx.Preimage, FeesPaid: tx.FeeMloki, FeeSkimMloki: tx.FeeSkimMloki}, nil
}
