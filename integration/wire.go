//go:build integration

package integration

import "github.com/flokiorg/lokihub/nip47/models"

// The structs below mirror the unexported request/response param shapes
// defined in nip47/controllers (which can't be imported directly from an
// external black-box test package) — kept intentionally minimal, matching
// only the JSON wire format actual NWC clients rely on.

// --- mint_cash ---
//
// A cash_wallet's connection is now shared by every recipient named in one
// mint_cash call — there is no more per-recipient encrypted reveal or
// separate claim_cash_wallet step. The plaintext pairing_uri comes back
// directly in the response, over the already end-to-end-encrypted NIP-47
// channel the hub itself is using.

type CashWalletRecipientParam struct {
	IdentityType  string `json:"identity_type"` // "pubkey" | "connection_key" | "bearer"
	IdentityValue string `json:"identity_value,omitempty"`
	IAPubkey      string `json:"ia_pubkey,omitempty"` // required iff identity_type == connection_key
	AmountMillis  uint64 `json:"amount_millis"`
}

type MintCashParams struct {
	Recipients []CashWalletRecipientParam `json:"recipients"`
	Expiry     int                        `json:"expiry,omitempty"`
	// MintSignature opts the issued token into mint provenance (§Mint Provenance).
	MintSignature bool `json:"mint_signature,omitempty"`
}

type CashWalletRecipientResult struct {
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value,omitempty"`
	AmountMillis  uint64 `json:"amount_millis"`
	BearerSecret  string `json:"bearer_secret,omitempty"`
}

type MintCashResult struct {
	WalletPubkey string                      `json:"wallet_pubkey"`
	PairingURI   string                      `json:"pairing_uri"`
	CashToken    string                      `json:"cash_token"`
	ExpiresAt    int64                       `json:"expires_at"`
	Recipients   []CashWalletRecipientResult `json:"recipients"`
}

// --- cash_redeem ---
//
// Replaces the old mint_cash/claim_cash_wallet two-step reveal flow:
// since the connection is already shared/known, a recipient just proves who
// they are (identity_event, bound to this wallet + this invoice — see
// nip47/controllers/cash_redeem_controller.go) and pays out their own slice
// in one call.

type ClaimFundsParams struct {
	Invoice          string  `json:"invoice"`
	Amount           *uint64 `json:"amount,omitempty"`
	IdentityType     string  `json:"identity_type,omitempty"`
	IdentityValue    string  `json:"identity_value,omitempty"`
	IdentityEvent    string  `json:"identity_event,omitempty"`
	AttestationEvent string  `json:"attestation_event,omitempty"`
	// BearerSecret redeems a bearer slice in place of every other field
	// above (NIP-JW §Bearer Slices).
	BearerSecret string `json:"bearer_secret,omitempty"`
}

type ClaimFundsResult struct {
	Preimage string `json:"preimage"`
	FeesPaid uint64 `json:"fees_paid"`
}

// --- cash_transfer ---
//
// Reassigns an unclaimed slice's registered identity without redeeming it
// (NIP-JW §Transferring a Slice). Proof scheme mirrors cash_redeem: an
// identity-bound caller proves who they currently are via IdentityEvent
// (bound to the wallet + the target new_identity, not an invoice); a bearer
// caller instead presents BearerSecret, since a bearer slice has no
// identity capable of signing a proof event.
//
// A bearer NewIdentity's IdentityValue is REQUIRED and caller-supplied — the
// commitment (sha256) of a secret the caller generates and keeps locally.
// The wallet never mints or returns a bearer secret here: this response
// travels over the shared cash_wallet connection, decryptable by every
// recipient who ever held it, so a server-generated secret returned in it
// would leak to all of them (see NIP-JW.md's Security Considerations).

type CashTransferNewIdentityParam struct {
	IdentityType  string `json:"identity_type"` // "pubkey" | "connection_key" | "bearer"
	IdentityValue string `json:"identity_value,omitempty"`
	IAPubkey      string `json:"ia_pubkey,omitempty"`
}

type CashTransferParams struct {
	IdentityType     string `json:"identity_type,omitempty"`
	IdentityValue    string `json:"identity_value,omitempty"`
	IdentityEvent    string `json:"identity_event,omitempty"`
	AttestationEvent string `json:"attestation_event,omitempty"`
	BearerSecret     string `json:"bearer_secret,omitempty"`

	NewIdentity CashTransferNewIdentityParam `json:"new_identity"`

	// AmountMillis is OPTIONAL — omitted, or equal to the slice's current full
	// amount, means "transfer it all". A value less than the slice's current
	// amount splits off exactly that much into a brand-new dedicated
	// cash_wallet, leaving the remainder behind under the SAME current
	// identity — see NIP-CASH §Splitting a Slice.
	AmountMillis *uint64 `json:"amount_millis,omitempty"`
}

// NewWalletPubkey/NewWalletToken are populated only when the transfer spun
// the slice off into a brand-new dedicated cash_wallet, rather than
// reassigning identity in place — see NIP-JW "Spinning a slice off into a
// dedicated wallet". NewWalletToken is a lokicash1... connection token,
// itself NIP-44 encrypted to the caller's own pubkey (the one that signed
// this call's IdentityEvent) using the new wallet's own keypair
// (NewWalletPubkey plus its matching server-held privkey) — a second, inner
// encryption layer nested inside this response's own normal per-connection
// encryption.
type CashTransferResult struct {
	AmountMillis  uint64 `json:"amount_millis"`
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value,omitempty"`
	// RemainingAmountMillis is populated only when this call went through the
	// split path: 0 for a full split, >0 for a partial one. Never populated
	// for an in-place reassignment.
	RemainingAmountMillis *uint64 `json:"remaining_amount_millis,omitempty"`
	NewWalletPubkey       string  `json:"new_wallet_pubkey,omitempty"`
	NewWalletToken        string  `json:"new_wallet_token,omitempty"`
	// RemainderWalletPubkey/RemainderWalletToken carry the caller's own change,
	// now in its own fresh dedicated wallet, for a PARTIAL split (NIP-CASH
	// §Splitting a Slice — the remainder is no longer left on the source
	// connection). Delivered the same nested-encrypted way as NewWalletToken.
	RemainderWalletPubkey string `json:"remainder_wallet_pubkey,omitempty"`
	RemainderWalletToken  string `json:"remainder_wallet_token,omitempty"`
}

// --- cash_consolidate ---
//
// Combines several same-hub slices this node custodies into one new cash token
// (NIP-CASH §Consolidating Tokens). Each source carries the same proof shapes
// cash_transfer accepts. v1: pubkey/bearer sources, pubkey new_identity.

type ConsolidateSourceParam struct {
	WalletPubkey  string `json:"wallet_pubkey"`
	IdentityType  string `json:"identity_type,omitempty"`
	IdentityValue string `json:"identity_value,omitempty"`
	IdentityEvent string `json:"identity_event,omitempty"`
	BearerSecret  string `json:"bearer_secret,omitempty"`
}

type CashConsolidateParams struct {
	Sources       []ConsolidateSourceParam     `json:"sources"`
	NewIdentity   CashTransferNewIdentityParam `json:"new_identity"`
	MintSignature bool                         `json:"mint_signature,omitempty"`
}

// CashConsolidateResult's NewWalletToken is the merged lokicash1... token,
// NIP-44 encrypted to new_identity using the merged wallet's own keypair
// (NewWalletPubkey + matching server-held privkey) — the same nested delivery a
// split uses.
type CashConsolidateResult struct {
	AmountMillis    uint64 `json:"amount_millis"`
	NewWalletPubkey string `json:"new_wallet_pubkey"`
	NewWalletToken  string `json:"new_wallet_token"`
	ExpiresAt       int64  `json:"expires_at,omitempty"`
}

// --- list_recipients ---

type RecipientStatus struct {
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value"`
	AmountMillis  int64  `json:"amount_millis"`
	Claimed       bool   `json:"claimed"`
	ClaimedAt     *int64 `json:"claimed_at,omitempty"`
	// RedeemFeeMillis/NetRedeemableMillis are this slice's cash_redeem quote —
	// the worst-case (external) fee and net payout, see NIP-CASH.md §Listing
	// Recipients.
	RedeemFeeMillis     int64  `json:"redeem_fee_millis"`
	NetRedeemableMillis int64  `json:"net_redeemable_millis"`
	MinTransferMillis   int64  `json:"min_transfer_millis"`
	ExpiresAt           *int64 `json:"expires_at,omitempty"`
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
