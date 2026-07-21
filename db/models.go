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
	AppKindJITHub       = "jit_hub"       // JIT Hub: issues pre-funded ephemeral jit_wallet children
	AppKindJITWallet    = "jit_wallet"    // ephemeral spend-only wallet issued by a JIT Hub
	AppKindCircleHub    = "circle_hub"    // Circle Hub: issues circle_wallet children to members
	AppKindCircleWallet = "circle_wallet" // sub-wallet issued to a circle member, starts with 0 balance
)

// Parent kinds — disambiguates JIT vs circle lineage in queries.
const (
	ParentKindJIT    = "jit"
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

	// Sub-wallet lineage (JIT and circle children)
	ParentAppID *uint  `gorm:"index:idx_apps_parent,priority:1"`
	ParentKind  string `gorm:"index:idx_apps_parent,priority:2"`

	// Expiry of this sub-wallet app (mirrors the AppPermission.ExpiresAt value for
	// efficient cleanup/commitment queries on the apps table itself).
	ExpiresAt *time.Time `gorm:"index:idx_apps_parent,priority:3"`

	// Cleanup state — set atomically before expiry sweep to prevent double-cleanup.
	CleanupInProgress bool
}

// JITHubConfig holds the per-JIT-Hub parameters that constrain what wallets may be issued.
// One row per jit_hub app; loaded on demand when create_jit_wallet is called.
type JITHubConfig struct {
	ID                uint `gorm:"primaryKey"`
	AppID             uint `gorm:"uniqueIndex;not null"`
	App               App  `gorm:"constraint:OnDelete:CASCADE;"`
	PerWalletMaxMloki int
	MaxExpSecs        int
}

// JIT allocation identity types.
const (
	JITAllocIdentityPubkey        = "pubkey"
	JITAllocIdentityConnectionKey = "connection_key"
)

// JITWalletClaim records one recipient's slice within a specific (possibly
// shared) jit_wallet app. A jit_wallet may serve several recipients from one
// funded pool and one NWC connection — each recipient gets their own row
// here (own identity, own AmountMloki), all sharing the wallet's single
// ExpiresAt (a property of the App itself, not duplicated here). claim_funds
// atomically flips ClaimedAt (guarded by "WHERE claimed_at IS NULL") to pay
// out a slice exactly once. (wallet_app_id, identity_type, identity_value)
// is unique: one slice per identity per wallet.
type JITWalletClaim struct {
	ID            uint   `gorm:"primaryKey"`
	WalletAppID   uint   `gorm:"not null;uniqueIndex:idx_jit_claim_wallet_identity,priority:1"`
	App           App    `gorm:"foreignKey:WalletAppID;constraint:OnDelete:CASCADE"`
	IdentityType  string `gorm:"not null;uniqueIndex:idx_jit_claim_wallet_identity,priority:2"` // "pubkey" | "connection_key"
	IdentityValue string `gorm:"not null;uniqueIndex:idx_jit_claim_wallet_identity,priority:3"` // 64-char hex
	// IAPubkey is set only for connection_key-mode slices — the Identity
	// Authority that must attest the claimant's identity at claim time.
	IAPubkey    string
	AmountMloki int64 `gorm:"not null"`
	ClaimedAt   *time.Time
	CreatedAt   time.Time
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
	// (required positive — mirrors JITHubConfig.PerWalletMaxMloki).
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
		app.Kind == AppKindJITHub ||
		app.Kind == AppKindJITWallet ||
		app.Kind == AppKindCircleHub ||
		app.Kind == AppKindCircleWallet
}

// IsIsolatedKind is a package-level helper for code paths that only have the kind string.
func IsIsolatedKind(kind string) bool {
	return kind == AppKindIsolated ||
		kind == AppKindJITHub ||
		kind == AppKindJITWallet ||
		kind == AppKindCircleHub ||
		kind == AppKindCircleWallet
}

// IsPrivilegedKind reports whether a kind is system-managed and must not have its
// scopes modified after creation via the generic UpdateApp path.
func IsPrivilegedKind(kind string) bool {
	return kind == AppKindJITHub ||
		kind == AppKindJITWallet ||
		kind == AppKindCircleHub ||
		kind == AppKindCircleWallet
}

// IsBudgetImmutableKind reports whether a kind's budget and expiry are
// system-managed and must not be changed via the generic UpdateApp path.
// Unlike IsPrivilegedKind, the hub kinds (AppKindCircleHub,
// AppKindJITHub) are excluded here: a hub's own budget/expiry are
// user-configurable like a regular app, it's only the wallets it issues that
// have limits coming from a dedicated flow (per-member allocation, per-wallet
// JIT config).
func IsBudgetImmutableKind(kind string) bool {
	return kind == AppKindJITWallet ||
		kind == AppKindCircleWallet
}

// IsNameImmutableKind reports whether a kind's name is system-generated
// (apps.GenerateChildName: "<hub> · <identity label> · <random>") and must
// not be changed via the generic UpdateApp path — the identity segment is
// what lets the UI resolve a Nostr profile name for display, so allowing a
// free-form rename would silently break that.
func IsNameImmutableKind(kind string) bool {
	return kind == AppKindJITWallet ||
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
	FeeSkimMloki    uint64
	PaymentRequest  string
	PaymentHash     string `gorm:"index"`
	Description     string
	DescriptionHash string
	Preimage        *string
	CreatedAt       time.Time
	ExpiresAt       *time.Time
	UpdatedAt       time.Time
	SettledAt       *time.Time
	Metadata        datatypes.JSON
	SelfPayment     bool
	Boostagram      datatypes.JSON
	FailureReason   string
	Hold            bool
	SettleDeadline  *uint32 // block number for accepted hold invoices
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
