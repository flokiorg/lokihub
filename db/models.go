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
	ID          uint
	Name        string `validate:"required"`
	Description string
	// AppPubkey/WalletPubkey carry a composite index because every incoming
	// NWC request resolves its target connection with
	// `WHERE app_pubkey = ? [AND wallet_pubkey = ?]` (nip47/event_handler.go).
	// Without it that lookup is a full apps-table scan on EVERY request — a cost
	// that grows with the table, which cash_wallet churn (each split spins off
	// two, each consolidate one) accelerates. app_pubkey leads the index so the
	// app_pubkey-only case (no p-tag) uses it as a prefix too.
	AppPubkey    string  `validate:"required" gorm:"not null;index:idx_apps_pubkey_lookup,priority:1"`
	WalletPubkey *string `gorm:"index:idx_apps_pubkey_lookup,priority:2"`
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
	// MaxExpSecs is the ceiling on how long an issued Cash wallet may remain
	// unredeemed, and the default a freshly-minted wallet gets when a
	// mint_cash caller omits its own expiry. 0 means "never" — no ceiling at
	// all, and a freshly-minted wallet with no caller-requested expiry never
	// expires (App.ExpiresAt nil, matching how every other nil-expiry
	// connection in this codebase is already treated). See
	// cashwallet.Resolve's expiry-resolution comment for the full rule.
	MaxExpSecs int
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
	// CashIdentityCash marks a slice with no registered identity at all
	// (NIP-CASH §Cash-Mode Slices) — redeemable by whoever presents its secret.
	CashIdentityCash = "cash"
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
	// IdentityType is "pubkey" | "connection_key" | "cash".
	IdentityType string `gorm:"not null;uniqueIndex:idx_cash_claim_wallet_identity,priority:2"`
	// IdentityValue is 64-char hex. For pubkey/connection_key slices this is
	// the identity itself (public, proof-gated at claim time). For a cash-mode
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
	// The payout facts for a slice redeemed over Lightning, recorded here at
	// redeem time rather than looked up later.
	//
	// They cannot be reconstructed after the fact: a Transaction carries no
	// identity, so on a multi-recipient bill there is nothing linking a payout
	// back to the slice that caused it — and for the common mint_cash shape,
	// where every recipient gets an equal amount, matching on amount and
	// timestamp is genuinely ambiguous. Worse, Transaction cascade-deletes with
	// the wallet app (see Transaction.App), so by the time a spent bill is
	// archived the ledger rows are already gone.
	//
	// All four stay zero for a slice that was split away rather than redeemed
	// (SpunOffToWalletAppID set), and for rows that predate this column.
	PaymentHash string `gorm:"index"`
	Preimage    string
	// RedeemFeeMloki is the hub's own cut, quoted from RedeemFeePpm and borne
	// by the recipient (deducted from their payout). RoutingFeeMloki is what
	// the hub paid the network to deliver it. They are different money moving
	// in different directions, so an audit record that kept only one would be
	// misleading — see transactions.reconcileCashRedeemFee, which settles the
	// difference between them.
	RedeemFeeMloki  int64
	RoutingFeeMloki int64
	SettledAt       *time.Time
	CreatedAt       time.Time
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

// CashStrandedFund is a durable record of one compensating-saga reversal that
// itself failed during cashwallet.Consolidate/SplitInTwo, so an operator can
// find and resolve it by querying data instead of grepping logs.
// SourceWalletAppID is the wallet whose contribution was debited but never
// returned; RetainedWalletAppID is the wallet deliberately left un-deleted
// (the merged wallet for a consolidate, the carved wallet for a split),
// still holding those funds. No foreign key to either App row: this record's
// own lifecycle is independent of whatever later happens to them (e.g. an
// operator manually sweeping and deleting the retained wallet must not
// silently erase the reconciliation history). ResolvedAt is set once an
// operator has manually swept the funds back — nil means still outstanding.
type CashStrandedFund struct {
	ID                  uint   `gorm:"primaryKey"`
	Operation           string `gorm:"not null"` // "consolidate" | "split"
	SourceWalletAppID   uint   `gorm:"not null;index"`
	RetainedWalletAppID uint   `gorm:"not null;index"`
	AmountMloki         uint64 `gorm:"not null"`
	CreatedAt           time.Time
	ResolvedAt          *time.Time `gorm:"index"`
}

// How a cash bill itself ended. Deliberately a different set from the
// slice-level vocabulary below: a bill whose slices ended differently — one
// redeemed, one split away — has no single slice value that describes it.
const (
	// CashBillOutcomeDrained: every slice reached a terminal state and the
	// balance hit zero, so the bill was auto-deleted rather than left holding
	// nothing.
	CashBillOutcomeDrained = "drained"
	// CashBillOutcomeExpired: the expiry sweep reclaimed it.
	CashBillOutcomeExpired = "expired"
	// CashBillOutcomeDeleted: an operator deleted it through the admin API.
	CashBillOutcomeDeleted = "deleted"
	// CashBillOutcomeWrittenOff: the parent hub was gone, so a remaining
	// balance could not be reclaimed anywhere and was abandoned.
	CashBillOutcomeWrittenOff = "written-off"
	// CashBillOutcomeVoid: a rollback of a bill whose funding (or whose
	// compensating reversal) failed. No recipient ever saw it, so it is kept
	// for forensics but excluded from listings by default.
	CashBillOutcomeVoid = "void"
)

// How one slice ended. This is the vocabulary shared by live and archived
// rows: a live row derives its value, an archived row stores the terminal one.
const (
	CashSliceStatusUnclaimed = "unclaimed" // live only: still redeemable
	CashSliceStatusRedeemed  = "redeemed"  // paid out over Lightning
	// CashSliceStatusSplit covers both a cash_transfer split and a
	// cash_consolidate: both move the value into another bill and are
	// indistinguishable from the claim row alone, which records only the
	// forward direction (SpunOffToWalletAppID).
	CashSliceStatusSplit = "split"
	// CashSliceStatusExpired: unclaimed when the bill's own window passed;
	// value reclaimed to the hub by the expiry sweep.
	CashSliceStatusExpired = "expired"
	// CashSliceStatusReclaimed: unclaimed when the bill was destroyed for some
	// other reason (operator delete, drained sibling). Distinct from expired —
	// the value still went back to the hub, but the window had not passed, so
	// conflating the two would hide a recipient who was cut off early.
	CashSliceStatusReclaimed = "reclaimed"
	// CashSliceStatusWrittenOff: unclaimed, and the value could not be
	// returned anywhere because the parent hub was gone.
	CashSliceStatusWrittenOff = "written-off"
	// CashSliceStatusVoid: belonged to a bill that never came into existence.
	CashSliceStatusVoid = "void"
)

// CashBillArchive is the durable record of one cash bill — a cash_wallet app —
// that has been hard-deleted.
//
// A spent bill MUST be hard-deleted: an unknown app is answered with silence
// (nip47.HandleEvent), which is what makes a spent bill indistinguishable from
// a pubkey this hub never served. But that delete cascades through
// CashWalletClaim and Transaction alike, so without this table a bill leaves no
// trace at all and the operator cannot account for money that passed through.
//
// No foreign key to any App row, for the same reason CashStrandedFund has
// none: every app this record names is already gone by the time it exists, so
// a constraint could only ever be a liability. Retained forever — this is the
// only remaining evidence the bill existed.
//
// WalletAppID is unique: app IDs are never reused, so it is this table's
// natural key and what makes a retried delete idempotent rather than
// double-archiving.
type CashBillArchive struct {
	ID uint `gorm:"primaryKey"`
	// WalletAppID is the id the bill's App row had. Unique — see above.
	WalletAppID uint `gorm:"not null;uniqueIndex"`
	// HubAppID scopes every listing query, and is the leading column of the
	// composite index below.
	HubAppID uint `gorm:"not null;index:idx_cash_bill_archive_hub_ended,priority:1"`
	// WalletPubkey is how an operator correlates this row with relay logs, and
	// what the silence-invariant test looks the bill up by.
	WalletPubkey string `gorm:"not null;index"`
	MintedAt     time.Time
	// EndedAt is when the bill was deleted. Second column of the composite
	// index so a per-hub listing is index-ordered rather than filesorting a
	// forever-growing table.
	EndedAt   time.Time `gorm:"not null;index:idx_cash_bill_archive_hub_ended,priority:2"`
	ExpiresAt *time.Time
	// Outcome is one of the CashBillOutcome* values.
	Outcome string `gorm:"not null;index"`
	// TotalMloki is the sum of this bill's archived slices, including any
	// archived earlier by an operator removing a single recipient — so it is
	// computed after those rows land, not from the claims alive at death.
	TotalMloki int64 `gorm:"not null"`
	// FundedMloki is the ledger truth: settled incoming transactions on the
	// bill, read before the cascade destroys them. Divergence from TotalMloki
	// is itself an auditable signal, so it is recorded rather than reconciled.
	FundedMloki int64 `gorm:"not null"`
	// ReclaimedMloki is what went back to the hub on the way out — non-zero
	// only for an expiry or operator delete that found a live balance.
	ReclaimedMloki int64
	// SplitFromWalletAppID mirrors App.SplitFromWalletAppID so a bill's
	// lineage survives the app row.
	SplitFromWalletAppID *uint
	CreatedAt            time.Time
}

// CashBillSliceArchive is the durable record of one CashWalletClaim that was
// destroyed — with its whole bill, or on its own when an operator removed a
// single recipient.
//
// Deliberately NOT a child of CashBillArchive, and FK-free like it. A slice can
// be archived while its bill is still very much alive (AppsService.
// DeleteCashClaim), so requiring a parent row would force a half-built bill
// record at that moment. Each row therefore carries HubAppID and WalletAppID
// directly and is self-sufficient for the merged listing; join on WalletAppID
// only when the full picture is wanted.
//
// The payout facts are denormalised off the claim rather than copying whole
// Transaction rows: those cascade away with the app anyway, and duplicating the
// ledger into an audit table buys nothing.
type CashBillSliceArchive struct {
	ID uint `gorm:"primaryKey"`
	// WalletAppID groups a bill's slices, and is what TotalMloki sums over.
	WalletAppID uint `gorm:"not null;index"`
	// HubAppID leads both composite indexes: it scopes every query, while
	// Outcome (7 values) and CreatedAt are useless as leading columns.
	HubAppID uint `gorm:"not null;index:idx_cash_slice_archive_hub_created,priority:1;index:idx_cash_slice_archive_hub_outcome,priority:1"`
	// ClaimID is the original CashWalletClaim.ID, kept only so a log line
	// naming a claim can still be resolved afterwards. Not unique here: claim
	// ids and archive ids are separate sequences.
	ClaimID uint `gorm:"not null"`

	// Identity is stored as-is, unhashed. For a cash-mode slice IdentityValue
	// is already a one-way commitment; for pubkey/connection_key it is public
	// information the hub owner could already see while the bill was live.
	IdentityType  string `gorm:"not null"`
	IdentityValue string `gorm:"not null;index"`
	IAPubkey      string
	AmountMloki   int64 `gorm:"not null"`

	// Outcome is one of the CashSliceStatus* values, derived per slice — never
	// copied down from the bill, since one bill's slices can end differently.
	Outcome string `gorm:"not null;index:idx_cash_slice_archive_hub_outcome,priority:2"`

	// CreatedAt is the ORIGINAL claim's CreatedAt, preserved so the merged
	// listing can sort live and archived rows on one comparable key.
	CreatedAt  time.Time `gorm:"index:idx_cash_slice_archive_hub_created,priority:2"`
	ClaimedAt  *time.Time
	ArchivedAt time.Time `gorm:"not null"`

	// Wallet-level facts denormalised so an archived row needs no join to
	// render in the listing.
	WalletPubkey    string
	WalletExpiresAt *time.Time

	MinTransferMloki int64
	RedeemFeePpm     int

	// Payout facts, populated only for Outcome == redeemed. RedeemFeeMloki is
	// the hub's own cut borne by the recipient; RoutingFeeMloki is what the hub
	// paid the network — see the same fields on CashWalletClaim.
	PaymentHash     string `gorm:"index"`
	Preimage        string
	RedeemFeeMloki  int64
	RoutingFeeMloki int64
	SettledAt       *time.Time

	// SpunOffToWalletAppID, for Outcome == split: which bill the value went to.
	SpunOffToWalletAppID *uint
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

// PublishesNip47InfoEvent reports whether an app of this kind should advertise
// itself to relays with a kind-13194 NIP-47 info event.
//
// Everything does except a cash_wallet. An info event is authored by the app's
// OWN wallet pubkey, so its presence on a relay is a permanent, unauthenticated
// answer to "did this hub ever serve this pubkey" — and its capability list
// (cash_redeem/cash_transfer/...) identifies it as a cash bill specifically.
// That defeats the hard delete a spent bill relies on (NIP-CASH §Lifecycle and
// Deletion), which exists precisely so a spent bill becomes indistinguishable
// from a pubkey the hub never served. Publishing and deleting later is no fix:
// relay-side deletion is best-effort over the network and can just fail.
//
// Not publishing costs a bill's holder nothing. The connection travels inside
// the lokicash token, which already carries everything needed to use it; a bill
// is never discovered by relay query. A cash_hub, by contrast, is a public
// service and advertising mint_cash is the point.
func PublishesNip47InfoEvent(kind string) bool {
	return kind != AppKindCashWallet
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
