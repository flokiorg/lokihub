package db

import (
	"time"

	"gorm.io/datatypes"
)

type UserConfig struct {
	ID        uint
	Key       string `gorm:"unique;not null"`
	Value     string
	Encrypted bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// App kinds — the "shape" of the connection.
const (
	AppKindStandard     = "standard"      // regular NWC connection, no own balance
	AppKindIsolated     = "isolated"      // sandboxed sub-wallet, own balance, no sub-issuance
	AppKindCashHub      = "cash_hub"      // Cash Hub: issues pre-funded ephemeral cash_wallet children
	AppKindCashWallet   = "cash_wallet"   // ephemeral spend-only wallet issued by a Cash Hub
	AppKindCircleHub    = "circle_hub"    // Circle Hub: issues circle_wallet children to members
	AppKindCircleWallet = "circle_wallet" // sub-wallet issued to a circle member, starts with 0 balance
)

// Parent kinds — disambiguates Cash vs circle lineage in queries.
const (
	ParentKindCash   = "cash"
	ParentKindCircle = "circle"
)

// Circle access policies. Only policies backed by a real, provider-controlled
// authorization decision are supported: "following" is provider-controlled
// (only the provider can add someone to their own contact list) and
// "allowlist" is explicit. A "followers" (or "both", which includes it)
// policy would check the *requester's* self-published contact list, which
// anyone can fabricate for free — it provides no real access control and is
// intentionally not offered.
const (
	CirclePolicyFollowing = "following"
	CirclePolicyAllowlist = "allowlist"
)

// Circle hub delete modes — how to handle circle_wallet children that
// still hold a nonzero balance when their circle_hub is deleted.
const (
	// CircleDeleteModeAll deletes the provider and every child, regardless of balance.
	CircleDeleteModeAll = "all"
	// CircleDeleteModeEmptyOnly deletes only zero-balance children. If any child still
	// has balance, the provider itself is left intact so the admin can retry later.
	CircleDeleteModeEmptyOnly = "empty_only"
)

type App struct {
	ID           uint
	Name         string `validate:"required"`
	Description  string
	AppPubkey    string `validate:"required" gorm:"not null"`
	WalletPubkey *string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastUsedAt   *time.Time
	Kind         string `gorm:"not null;default:'standard'"`
	Metadata     datatypes.JSON

	// Sub-wallet lineage (Cash and circle children)
	ParentAppID *uint  `gorm:"index:idx_apps_parent,priority:1"`
	ParentKind  string `gorm:"index:idx_apps_parent,priority:2"`

	// Expiry of this sub-wallet app (mirrors the AppPermission.ExpiresAt value for
	// efficient cleanup/commitment queries on the apps table itself).
	ExpiresAt *time.Time `gorm:"index:idx_apps_parent,priority:3"`

	// Cleanup state — set atomically before expiry sweep to prevent double-cleanup.
	CleanupInProgress bool

	// SplitFromWalletAppID is set once, right after funding succeeds, when
	// this cash_wallet was created by splitting a partial or full amount off
	// an existing cash_wallet's slice (cashwallet.Split) rather than minted
	// directly by a Cash Hub. Purely informational — the reverse of
	// CashWalletClaim.SpunOffToWalletAppID (that column records, on the
	// SOURCE slice, which new wallet its value moved to; this one records,
	// on the NEW wallet, which wallet it came from). Never read by any
	// atomic guard, only by callers wanting to show lineage.
	SplitFromWalletAppID *uint
}

// CashHubConfig holds the per-Cash-Hub parameters that constrain what
// wallets may be issued. One row per cash_hub app; loaded on demand when
// mint_cash is called.
type CashHubConfig struct {
	ID                uint `gorm:"primaryKey"`
	AppID             uint `gorm:"uniqueIndex;not null"`
	App               App  `gorm:"constraint:OnDelete:CASCADE;"`
	PerWalletMaxMloki int
	MaxExpSecs        int
	// MinTransferMloki is the default floor (0 = no floor) applied to every
	// recipient's slice when a Cash Hub freshly mints a wallet — see
	// CashWalletClaim.MinTransferMloki for how it's inherited from there on.
	MinTransferMloki int64
	// RedeemFeePpm is the default per-million fee (0 = free) applied to every
	// recipient's slice when a Cash Hub freshly mints a wallet, charged only
	// on a genuine external cash_redeem (never on a same-node redemption, and
	// never on cash_transfer) — see CashWalletClaim.RedeemFeePpm for how it's
	// inherited from there on. Same validation/semantics as
	// CircleHubConfig.FeesPpm (0 <= x <= constants.MAX_FEES_PPM).
	RedeemFeePpm int
}

// Cash allocation identity types.
const (
	CashIdentityPubkey        = "pubkey"
	CashIdentityConnectionKey = "connection_key"
	// CashIdentityBearer marks a slice with no registered identity at all
	// (NIP-CASH §Bearer Slices) — redeemable by whoever presents its secret.
	CashIdentityBearer = "bearer"
)

// CashWalletClaim records one recipient's slice within a specific (possibly
// shared) cash_wallet app. A cash_wallet may serve several recipients from
// one funded pool and one NWC connection — each recipient gets their own row
// here (own identity, own AmountMloki), all sharing the wallet's single
// ExpiresAt (a property of the App itself, not duplicated here). cash_redeem
// atomically flips ClaimedAt (guarded by "WHERE claimed_at IS NULL") to pay
// out a slice exactly once. (wallet_app_id, identity_type, identity_value)
// is unique: one slice per identity per wallet.
type CashWalletClaim struct {
	ID          uint `gorm:"primaryKey"`
	WalletAppID uint `gorm:"not null;uniqueIndex:idx_cash_claim_wallet_identity,priority:1"`
	App         App  `gorm:"foreignKey:WalletAppID;constraint:OnDelete:CASCADE"`
	// IdentityType is "pubkey" | "connection_key" | "bearer".
	IdentityType string `gorm:"not null;uniqueIndex:idx_cash_claim_wallet_identity,priority:2"`
	// IdentityValue is 64-char hex. For pubkey/connection_key slices this is
	// the identity itself (public, proof-gated at claim time). For a bearer
	// slice it is instead a one-way SHA-256 commitment of the slice's
	// secret — the raw secret is never persisted, only ever returned once,
	// in the mint_cash/cash_transfer response that generated it.
	IdentityValue string `gorm:"not null;uniqueIndex:idx_cash_claim_wallet_identity,priority:3"`
	// IAPubkey is set only for connection_key-mode slices — the Identity
	// Authority that must attest the claimant's identity at claim time.
	// Indexed so ListIdentityAuthorities' unredeemed-slice-count query (grouped
	// on this column, filtered to claimed_at IS NULL) doesn't full-scan.
	IAPubkey    string `gorm:"index"`
	AmountMloki int64  `gorm:"not null"`
	ClaimedAt   *time.Time
	// TransferCount is an internal optimistic-concurrency version number,
	// incremented atomically by every cash_transfer (reassignment or split)
	// against this row — pinned in each atomic update's WHERE clause so two
	// concurrent transfers of the same slice can never both succeed (see
	// AppsService.ReassignCashSliceIdentity/SplitCashSliceAmount). It is not
	// a user-facing cap (there is no transfer limit): a wallet created by
	// splitting starts its own TransferCount at 0, since it's a fresh row
	// with its own concurrency history, not a continuation of the source
	// slice's.
	TransferCount int
	// MinTransferMloki floors how small an amount this slice may be split
	// into (the carved-off piece) or leave behind (the remainder) via
	// cash_transfer — 0 means no floor. Set once: from the hub's config for
	// a freshly-minted wallet, or inherited from the source slice for a
	// split-off wallet.
	MinTransferMloki int64
	// RedeemFeePpm is this slice's own per-million cash_redeem fee (0 =
	// free), snapshotted once at the moment the slice was created — from the
	// hub's current RedeemFeePpm default for a freshly-minted wallet, or
	// inherited unchanged from the source slice for a split-off wallet —
	// exactly like MinTransferMloki above. Deliberately immutable
	// thereafter, including across an in-place identity reassignment
	// (ReassignCashSliceIdentity never touches this column): a later change
	// to the hub's own config must never retroactively change the rate for
	// an already-issued lokicash, and the rate must stay identical for every
	// future owner the slice passes through via cash_transfer. Charged only
	// on a genuine external cash_redeem (see
	// transactions.reconcileCashRedeemFee) — a same-node redemption always
	// pays out the slice's full AmountMloki with no fee.
	RedeemFeePpm int
	// SpunOffToWalletAppID is set (alongside ClaimedAt) when this slice's
	// entire value was moved into a brand-new dedicated cash_wallet rather
	// than redeemed via a real Lightning payment — see
	// AppsService.SplitCashSliceAmount. Purely informational: every atomic
	// guard elsewhere already treats ClaimedAt != nil as terminal regardless
	// of which mechanism set it, so this column is never read by any guard,
	// only by callers (e.g. list_recipients) that want to explain *why* a
	// slice is claimed with no matching payment record.
	SpunOffToWalletAppID *uint
	CreatedAt            time.Time
}

// CircleIdentity is a reusable Nostr identity (policy + provider pubkey +
// allowlist) that one or more circle_hub apps can reference. It has no FK
// to any App — deleting every circle_hub that references it leaves the
// identity (and its allowlist) fully intact, and multiple circle_hub apps
// may reference the same identity concurrently (e.g. two circles with
// different fee/budget structures sharing one trusted membership list).
type CircleIdentity struct {
	ID             uint   `gorm:"primaryKey"`
	Name           string `gorm:"not null"`
	Policy         string `gorm:"index"` // queried every tick by GetFollowingCircleIdentities
	ProviderPubkey string
}

// CircleIdentityAllowedPubkey records which nostr pubkeys are authorized under
// an allowlist-policy CircleIdentity. Cascade-deletes only when the identity
// itself is deleted — never when a circle_hub app referencing it is deleted.
type CircleIdentityAllowedPubkey struct {
	ID               uint
	CircleIdentityID uint           `gorm:"not null;index:idx_circle_identity_allowed_pubkeys_id_pubkey,priority:1"`
	CircleIdentity   CircleIdentity `gorm:"constraint:OnDelete:CASCADE;"`
	Pubkey           string         `gorm:"not null;index:idx_circle_identity_allowed_pubkeys_id_pubkey,priority:2"`
	// CreatedAt lets buildCircleIdentityCounts report "last policy update" for
	// allowlist-policy identities as MAX(created_at) across their rows — since
	// ReplaceCircleAllowlist deletes and re-inserts the whole set on every edit
	// or relay refresh, this doubles as "when the membership was last touched."
	CreatedAt time.Time
}

// CircleHubConfig holds the per-Circle-Provider deployment parameters
// (budget/expiry terms — not identity/authorization, which lives on the
// referenced CircleIdentity so it can be shared across providers).
// One row per circle_hub app; loaded on demand when create_circle_wallet is called.
type CircleHubConfig struct {
	ID               uint `gorm:"primaryKey"`
	AppID            uint `gorm:"uniqueIndex;not null"`
	App              App  `gorm:"constraint:OnDelete:CASCADE;"`
	CircleIdentityID uint `gorm:"not null;index"`
	// No OnDelete:CASCADE here — deleting this config (i.e. deleting the
	// circle_hub app) must never delete the shared identity.
	CircleIdentity CircleIdentity
	MaxExpSecs     int
	FeesPpm        int
	// PerWalletMaxMloki caps a caller's requested max_amount per issued wallet
	// (required positive — mirrors CashHubConfig.PerWalletMaxMloki).
	PerWalletMaxMloki int
	// MinBudgetRenewal is the shortest (tightest) renewal period a caller may
	// request for their wallet's budget_renewal — protects the hub from
	// members resetting their spend cap too often. A request is rejected when
	// its constants.BudgetRenewalRank is tighter (lower) than this floor's
	// rank (e.g. floor "monthly" allows "monthly"/"yearly"/"never", rejects
	// "daily"/"weekly").
	MinBudgetRenewal string
}

// CircleWalletIdentityProof records the nostr event ID of every consumed
// create_circle_wallet identity proof, so a captured proof (the circle_hub
// connection is shared/public — anyone holding it can decrypt every request
// sent over it, including this one) can't be resubmitted to mint repeat
// wallets within its own freshness window. EventID is globally unique
// (content-addressed hash), so no additional scoping key is needed for
// correctness.
type CircleWalletIdentityProof struct {
	ID        uint   `gorm:"primaryKey"`
	AppID     uint   `gorm:"not null;index"` // the circle_hub, for observability only
	EventID   string `gorm:"not null;uniqueIndex"`
	CreatedAt time.Time
}

// CashTransferProof records the nostr event ID of every consumed
// cash_transfer identity proof, so a captured proof (a multi-recipient
// cash_wallet connection is shared — every co-recipient can decrypt every
// request sent over it, including this one) can't be resubmitted to
// authorize a repeat transfer/split, or one for a different amount_mloki
// than it was signed for, within its own freshness window. A partial split
// doesn't change the source slice's registered identity the way an in-place
// reassignment does, so — unlike a full transfer — state alone doesn't
// naturally invalidate a replayed proof for the split case; this table is
// what does. EventID is globally unique (content-addressed hash), so no
// additional scoping key is needed for correctness. See
// verifyTransferIdentityEvent's doc comment (cash_transfer_controller.go).
type CashTransferProof struct {
	ID        uint   `gorm:"primaryKey"`
	AppID     uint   `gorm:"not null;index"` // the cash_wallet, for observability only
	EventID   string `gorm:"not null;uniqueIndex"`
	CreatedAt time.Time
}

// CircleWalletMembership enforces at most one *active* circle_wallet per
// (circle_hub, identity) at a time. Cascade-deletes when the child Wallet App
// row is deleted — expiry sweep, manual per-child delete, or hub teardown —
// which is what frees the identity to mint a new wallet later. Scoped to the
// hub, not the (possibly shared) CircleIdentity, matching "one wallet under
// THIS hub" rather than "one wallet across every hub using this identity."
type CircleWalletMembership struct {
	ID              uint   `gorm:"primaryKey"`
	CircleHubAppID  uint   `gorm:"not null;uniqueIndex:idx_circle_membership_hub_pubkey,priority:1"`
	CircleHub       App    `gorm:"foreignKey:CircleHubAppID;constraint:OnDelete:CASCADE"`
	RequesterPubkey string `gorm:"not null;uniqueIndex:idx_circle_membership_hub_pubkey,priority:2"`
	WalletAppID     uint   `gorm:"not null"`
	Wallet          App    `gorm:"foreignKey:WalletAppID;constraint:OnDelete:CASCADE"`
	CreatedAt       time.Time
}

// IsIsolated returns true for all app kinds that maintain their own balance.
func (app *App) IsIsolated() bool {
	return app.Kind == AppKindIsolated ||
		app.Kind == AppKindCashHub ||
		app.Kind == AppKindCashWallet ||
		app.Kind == AppKindCircleHub ||
		app.Kind == AppKindCircleWallet
}

// IsIsolatedKind is a package-level helper for code paths that only have the kind string.
func IsIsolatedKind(kind string) bool {
	return kind == AppKindIsolated ||
		kind == AppKindCashHub ||
		kind == AppKindCashWallet ||
		kind == AppKindCircleHub ||
		kind == AppKindCircleWallet
}

// IsPrivilegedKind reports whether a kind is system-managed and must not have its
// scopes modified after creation via the generic UpdateApp path.
func IsPrivilegedKind(kind string) bool {
	return kind == AppKindCashHub ||
		kind == AppKindCashWallet ||
		kind == AppKindCircleHub ||
		kind == AppKindCircleWallet
}

// IsBudgetImmutableKind reports whether a kind's budget and expiry are
// system-managed and must not be changed via the generic UpdateApp path.
// Unlike IsPrivilegedKind, the hub kinds (AppKindCircleHub,
// AppKindCashHub) are excluded here: a hub's own budget/expiry are
// user-configurable like a regular app, it's only the wallets it issues that
// have limits coming from a dedicated flow (per-member allocation, per-wallet
// Cash config).
func IsBudgetImmutableKind(kind string) bool {
	return kind == AppKindCashWallet ||
		kind == AppKindCircleWallet
}

// IsNameImmutableKind reports whether a kind's name is system-generated
// (apps.GenerateChildName: "<hub> · <identity label> · <random>") and must
// not be changed via the generic UpdateApp path — the identity segment is
// what lets the UI resolve a Nostr profile name for display, so allowing a
// free-form rename would silently break that.
func IsNameImmutableKind(kind string) bool {
	return kind == AppKindCashWallet ||
		kind == AppKindCircleWallet
}

type AppPermission struct {
	ID            uint
	AppId         uint   `validate:"required"`
	App           App    `gorm:"constraint:OnDelete:CASCADE;"`
	Scope         string `validate:"required"`
	MaxAmountLoki int
	BudgetRenewal string
	ExpiresAt     *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type RequestEvent struct {
	ID          uint
	AppId       *uint
	App         App    `gorm:"constraint:OnDelete:CASCADE;"`
	NostrId     string `validate:"required" gorm:"unique;not null"`
	ContentData string
	Method      string
	State       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type ResponseEvent struct {
	ID           uint
	NostrId      string       `validate:"required" gorm:"unique;not null"`
	RequestId    uint         `validate:"required"`
	RequestEvent RequestEvent `gorm:"constraint:OnDelete:CASCADE;foreignKey:RequestId"`
	State        string
	RepliedAt    time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Transaction struct {
	ID              uint
	AppId           *uint `gorm:"index:idx_transactions_app_type_state,priority:1"`
	App             *App  `gorm:"constraint:OnDelete:CASCADE;"`
	RequestEventId  *uint
	RequestEvent    *RequestEvent `gorm:"constraint:OnDelete:SET NULL;foreignKey:RequestEventId"`
	Type            string        `gorm:"index:idx_transactions_app_type_state,priority:2"`
	State           string        `gorm:"index:idx_transactions_app_type_state,priority:3"`
	AmountMloki     uint64        `gorm:"column:amount_mloki"`
	FeeMloki        uint64
	FeeReserveMloki uint64
	// FeeSkimMloki is a circle_hub's forwarding-fee cut (CircleHubConfig.FeesPpm
	// of AmountMloki) on an outgoing payment made by one of its circle_wallet
	// children. Set once at payment initiation (unlike FeeReserveMloki, it is
	// never reset to 0 — it's a real, permanent charge, not transient headroom)
	// and included alongside FeeMloki/FeeReserveMloki in every isolated-balance
	// and budget-usage calculation. Zero for every other transaction.
	FeeSkimMloki uint64
	// CashRedeemFeeMloki is set only on a cash_wallet's own outgoing payout
	// row for a cash_redeem call — the redeem fee quoted to (and deducted
	// from) the recipient's payout, computed by the controller from the
	// slice's own CashWalletClaim.RedeemFeePpm before the payment is even
	// attempted (0 for a same-node redemption). Deliberately NOT read by
	// db/queries.GetIsolatedBalance — unlike FeeSkimMloki, it must never be
	// double-counted against the wallet's balance, since it's already priced
	// into AmountMloki (a fee-reduced net payout, not an addition on top).
	// Nil means "not a cash_redeem payout, nothing to reconcile"; a non-nil
	// pointer (including a nil-vs-zero-value pointer, so an explicit 0 fee
	// still triggers reconciliation) is what
	// transactions.reconcileCashRedeemFee gates on at settlement time to
	// move the delta between this fee and the real routing fee between the
	// wallet and its parent Cash Hub — see that function's doc comment for
	// why the shared wallet's balance is never affected by either fee once
	// this reconciliation lands.
	CashRedeemFeeMloki *uint64
	PaymentRequest     string
	PaymentHash        string `gorm:"index"`
	Description        string
	DescriptionHash    string
	Preimage           *string
	CreatedAt          time.Time
	ExpiresAt          *time.Time
	UpdatedAt          time.Time
	SettledAt          *time.Time
	Metadata           datatypes.JSON
	SelfPayment        bool
	Boostagram         datatypes.JSON
	FailureReason      string
	Hold               bool
	SettleDeadline     *uint32 // block number for accepted hold invoices
}

type Swap struct {
	ID                 uint
	SwapId             string `validate:"required" gorm:"unique;not null"`
	Type               string
	State              string
	Invoice            string
	SendAmount         uint64
	ReceiveAmount      uint64
	Preimage           string
	PaymentHash        string
	DestinationAddress string
	RefundAddress      string
	LockupAddress      string
	LockupTxId         string
	ClaimTxId          string
	AutoSwap           bool
	UsedXpub           bool
	TimeoutBlockHeight uint32
	BoltzPubkey        string
	SwapTree           datatypes.JSON
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Forward struct {
	ID                           uint
	OutboundAmountForwardedMloki uint64
	TotalFeeEarnedMloki          uint64
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

const (
	REQUEST_EVENT_STATE_HANDLER_EXECUTING = "executing"
	REQUEST_EVENT_STATE_HANDLER_EXECUTED  = "executed"
	REQUEST_EVENT_STATE_HANDLER_ERROR     = "error"
)
const (
	RESPONSE_EVENT_STATE_PUBLISH_CONFIRMED   = "confirmed"
	RESPONSE_EVENT_STATE_PUBLISH_FAILED      = "failed"
	RESPONSE_EVENT_STATE_PUBLISH_UNCONFIRMED = "unconfirmed"
)
