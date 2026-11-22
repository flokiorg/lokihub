package mcp

import (
	"github.com/flokiorg/lokihub/api"
	"github.com/flokiorg/lokihub/cash/cashorder"
)

// MCP output views. Every tool that would otherwise return a core struct
// carrying a credential returns one of these instead. They are ALLOW-lists —
// new fields added to the core structs do not leak until someone adds them
// here on purpose — and TestNoSecretsInToolOutputs fails the build if a
// secret-shaped field ever reaches an output schema.
//
// Redacted:
//   - cash_token: a live bearer cash bill — whoever holds it can spend it
//     (audit CRIT-1).
//   - preimage on invoices we issue / incoming payments: an unpaid invoice's
//     preimage lets a routing node settle without forwarding.
//   - connectionString on order connections: a pairing secret (audit MED-4).
//   - metadata: an untyped map that can carry internal payment flags.

// txView is a Lightning transaction without its preimage or raw metadata.
type txView struct {
	Type            string          `json:"type"`
	State           string          `json:"state"`
	Invoice         string          `json:"invoice"`
	Description     string          `json:"description_untrusted"` // payer/payee-written memo
	DescriptionHash string          `json:"descriptionHash"`
	PaymentHash     string          `json:"paymentHash"`
	Amount          uint64          `json:"amount"`
	FeesPaid        uint64          `json:"feesPaid"`
	FeeSkim         uint64          `json:"feeSkim,omitempty"`
	UpdatedAt       string          `json:"updatedAt"`
	CreatedAt       string          `json:"createdAt"`
	SettledAt       *string         `json:"settledAt"`
	AppID           *uint           `json:"appId"`
	Boostagram      *api.Boostagram `json:"boostagram_untrusted,omitempty"` // sender-written
	FailureReason   string          `json:"failureReason"`
}

func toTxView(t *api.Transaction) txView {
	if t == nil {
		return txView{}
	}
	return txView{
		Type: t.Type, State: t.State, Invoice: t.Invoice, Description: t.Description,
		DescriptionHash: t.DescriptionHash, PaymentHash: t.PaymentHash, Amount: t.Amount,
		FeesPaid: t.FeesPaid, FeeSkim: t.FeeSkim, UpdatedAt: t.UpdatedAt, CreatedAt: t.CreatedAt,
		SettledAt: t.SettledAt, AppID: t.AppId, Boostagram: t.Boostagram, FailureReason: t.FailureReason,
	}
}

type txList struct {
	TotalCount   uint64   `json:"totalCount"`
	Transactions []txView `json:"transactions"`
}

func toTxList(r *api.ListTransactionsResponse) txList {
	if r == nil {
		return txList{Transactions: []txView{}}
	}
	out := txList{TotalCount: r.TotalCount, Transactions: make([]txView, 0, len(r.Transactions))}
	for i := range r.Transactions {
		out.Transactions = append(out.Transactions, toTxView(&r.Transactions[i]))
	}
	return out
}

// paymentView is an outgoing payment result: the proof of payment is the one
// preimage that is ours to show (it proves WE paid).
type paymentView struct {
	txView
	PaymentProof *string `json:"payment_proof,omitempty" jsonschema:"proof that this payment was made (its preimage)"`
}

func toPaymentView(t *api.Transaction) paymentView {
	if t == nil {
		return paymentView{}
	}
	return paymentView{txView: toTxView(t), PaymentProof: t.Preimage}
}

// claimView is a cash-wallet recipient slice without its cash token.
type claimView struct {
	ID                   uint   `json:"id"`
	WalletAppID          uint   `json:"wallet_app_id"`
	IdentityType         string `json:"identity_type"`
	IdentityValue        string `json:"identity_value"`
	AmountMloki          int64  `json:"amount_mloki"`
	ExpiresAt            *int64 `json:"expires_at,omitempty"`
	Claimed              bool   `json:"claimed"`
	ClaimedAt            *int64 `json:"claimed_at,omitempty"`
	CreatedAt            int64  `json:"created_at"`
	MinTransferMloki     int64  `json:"min_transfer_mloki"`
	RedeemFeePpm         int    `json:"redeem_fee_ppm"`
	SpunOffToWalletAppID *uint  `json:"spun_off_to_wallet_app_id,omitempty"`
	Status               string `json:"status"`
	Archived             bool   `json:"archived"`
	WalletPubkey         string `json:"wallet_pubkey,omitempty"`
	SettledAt            *int64 `json:"settled_at,omitempty"`
	RedeemFeeMloki       int64  `json:"redeem_fee_mloki,omitempty"`
}

func toClaimViews(in []api.CashWalletClaimResponse) []claimView {
	out := make([]claimView, 0, len(in))
	for _, c := range in {
		out = append(out, claimView{
			ID: c.ID, WalletAppID: c.WalletAppID, IdentityType: c.IdentityType, IdentityValue: c.IdentityValue,
			AmountMloki: c.AmountMloki, ExpiresAt: c.ExpiresAt, Claimed: c.Claimed, ClaimedAt: c.ClaimedAt,
			CreatedAt: c.CreatedAt, MinTransferMloki: c.MinTransferMloki, RedeemFeePpm: c.RedeemFeePpm,
			SpunOffToWalletAppID: c.SpunOffToWalletAppID, Status: c.Status, Archived: c.Archived,
			WalletPubkey: c.WalletPubkey, SettledAt: c.SettledAt, RedeemFeeMloki: c.RedeemFeeMloki,
		})
	}
	return out
}

// orderConnView is a selling (order) connection without its pairing string.
// The owner copies the string from the hub UI when publishing a listing.
type orderConnView struct {
	ID           uint   `json:"id"`
	HubID        uint   `json:"hubId"`
	Label        string `json:"label"`
	WalletPubkey string `json:"walletPubkey"`
	RetiredAt    *int64 `json:"retiredAt,omitempty"`
	CreatedAt    int64  `json:"createdAt"`
}

func toOrderConnView(c cashorder.Connection) orderConnView {
	return orderConnView{ID: c.AppID, HubID: c.HubAppID, Label: c.Label, WalletPubkey: c.WalletPubkey, RetiredAt: c.RetiredAt, CreatedAt: c.CreatedAt}
}
