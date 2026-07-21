//go:build integration

package integration

import "github.com/flokiorg/lokihub/nip47/models"

// The structs below mirror the unexported request/response param shapes
// defined in nip47/controllers (which can't be imported directly from an
// external black-box test package) — kept intentionally minimal, matching
// only the JSON wire format actual NWC clients rely on.

// --- create_jit_wallet ---
//
// A jit_wallet's connection is now shared by every recipient named in one
// create_jit_wallet call — there is no more per-recipient encrypted reveal or
// separate claim_jit_wallet step. The plaintext pairing_uri comes back
// directly in the response, over the already end-to-end-encrypted NIP-47
// channel the hub itself is using.

type JITWalletRecipientParam struct {
	IdentityType  string `json:"identity_type"` // "pubkey" | "connection_key"
	IdentityValue string `json:"identity_value"`
	IAPubkey      string `json:"ia_pubkey,omitempty"` // required iff identity_type == connection_key
	AmountMloki   uint64 `json:"amount_mloki"`
}

type CreateJITWalletParams struct {
	Recipients []JITWalletRecipientParam `json:"recipients"`
	Expiry     int                       `json:"expiry,omitempty"`
}

type JITWalletRecipientResult struct {
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value"`
	AmountMloki   uint64 `json:"amount_mloki"`
}

type CreateJITWalletResult struct {
	WalletPubkey string                     `json:"wallet_pubkey"`
	PairingURI   string                     `json:"pairing_uri"`
	ExpiresAt    int64                      `json:"expires_at"`
	Recipients   []JITWalletRecipientResult `json:"recipients"`
}

// --- claim_funds ---
//
// Replaces the old create_jit_wallet/claim_jit_wallet two-step reveal flow:
// since the connection is already shared/known, a recipient just proves who
// they are (identity_event, bound to this wallet + this invoice — see
// nip47/controllers/claim_funds_controller.go) and pays out their own slice
// in one call.

type ClaimFundsParams struct {
	Invoice          string  `json:"invoice"`
	Amount           *uint64 `json:"amount,omitempty"`
	IdentityType     string  `json:"identity_type"`
	IdentityValue    string  `json:"identity_value"`
	IdentityEvent    string  `json:"identity_event"`
	AttestationEvent string  `json:"attestation_event,omitempty"`
}

type ClaimFundsResult struct {
	Preimage string `json:"preimage"`
	FeesPaid uint64 `json:"fees_paid"`
}

// --- list_recipients ---

type RecipientStatus struct {
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value"`
	AmountMloki   int64  `json:"amount_mloki"`
	Claimed       bool   `json:"claimed"`
	ClaimedAt     *int64 `json:"claimed_at,omitempty"`
}

type ListRecipientsResult struct {
	Recipients []RecipientStatus `json:"recipients"`
}

// --- create_circle_wallet ---

type CreateCircleWalletParams struct {
	Pubkey        string `json:"pubkey"`
	MaxAmount     uint64 `json:"max_amount"`
	Expiry        int    `json:"expiry"`
	BudgetRenewal string `json:"budget_renewal,omitempty"`
	// IdentityEvent is a JSON-encoded, freshly-signed kind-35521 proof that
	// the caller controls Pubkey, bound to this specific hub via its d-tag
	// (see nip47/controllers/create_circle_wallet_identity.go).
	IdentityEvent string `json:"identity_event"`
}

type CreateCircleWalletResult struct {
	EncryptedPairingURI string `json:"encrypted_pairing_uri"`
	WalletPubkey        string `json:"wallet_pubkey"`
	ExpiresAt           int64  `json:"expires_at"`
	FeesPpm             int    `json:"fees_ppm"`
	BudgetRenewal       string `json:"budget_renewal"`
}

// --- generic NWC methods ---

type GetBalanceResult struct {
	Balance int64 `json:"balance"`
}

type GetBudgetResult struct {
	UsedBudget    uint64  `json:"used_budget"`
	TotalBudget   uint64  `json:"total_budget"`
	RenewsAt      *uint64 `json:"renews_at,omitempty"`
	RenewalPeriod string  `json:"renewal_period"`
}

type GetInfoResult struct {
	Alias         *string           `json:"alias"`
	Pubkey        *string           `json:"pubkey"`
	Network       *string           `json:"network"`
	Methods       []string          `json:"methods"`
	Notifications []string          `json:"notifications"`
	CircleWallet  *CircleWalletInfo `json:"circle_wallet,omitempty"`
}

// CircleWalletInfo mirrors nip47/controllers/get_info_controller.go's
// circleWalletInfo — only present on a circle_hub's own get_info response.
type CircleWalletInfo struct {
	AvailableMloki int64  `json:"available_mloki"`
	MaxExpSecs     int    `json:"max_exp_secs"`
	FeesPpm        int    `json:"fees_ppm"`
	CirclePolicy   string `json:"circle_policy"`
}

type MakeInvoiceParams struct {
	Amount          uint64                 `json:"amount"`
	Description     string                 `json:"description,omitempty"`
	DescriptionHash string                 `json:"description_hash,omitempty"`
	Expiry          uint64                 `json:"expiry,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

type MakeInvoiceResult = models.Transaction

type PayInvoiceParams struct {
	Invoice string  `json:"invoice"`
	Amount  *uint64 `json:"amount,omitempty"`
}

type PayInvoiceResult struct {
	Preimage string `json:"preimage"`
	FeesPaid uint64 `json:"fees_paid"`
}

type ListTransactionsParams struct {
	From   uint64 `json:"from,omitempty"`
	Until  uint64 `json:"until,omitempty"`
	Limit  uint64 `json:"limit,omitempty"`
	Offset uint64 `json:"offset,omitempty"`
}

type ListTransactionsResult struct {
	Transactions []models.Transaction `json:"transactions"`
	TotalCount   uint64               `json:"total_count"`
}
