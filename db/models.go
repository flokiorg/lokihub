package db

import (
	"fmt"
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

// Circle access policies (NIP-CW §Membership). Both are provider-controlled:
// "follow_set" admits the members of one NIP-51 follow set the provider
// publishes, and "allowlist" is explicit. A "followers" policy would check
// the *requester's* self-published lists, which anyone can fabricate for
// free, so it is intentionally not offered.
const (
	CirclePolicyFollowSet = "follow_set"
	CirclePolicyAllowlist = "allowlist"
)

// KindFollowSet is NIP-51's follow set, the event a follow_set circle reads.
const KindFollowSet = 30000

// Circle hub delete modes — how to handle circle_wallet children that
// still hold a nonzero balance when their circle_hub is deleted.
const (
	// CircleDeleteModeAll sweeps every child's balance to the provider and the
	// provider's to the main balance, then deletes them all. A child with a
	// payment in flight keeps the provider intact until it ends.
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
	//
	// WalletPubkey carries a SECOND, standalone index because the private transport
	// resolves an app the other way round: an envelope's outer key is a fresh
	// ephemeral one, so there is no app_pubkey to look up, and each item names the
	// WALLET pubkey in its target. A composite led by app_pubkey cannot serve
	// `WHERE wallet_pubkey = ?` — the leading column is absent — so that lookup
	// would full-scan apps once PER ITEM, on exactly the table this comment already
	// notes grows with every split and consolidate.
	AppPubkey    string  `validate:"required" gorm:"not null;index:idx_apps_pubkey_lookup,priority:1"`
	WalletPubkey *string `gorm:"index:idx_apps_pubkey_lookup,priority:2;index:idx_apps_wallet_pubkey"`
	// CashToken is a cash_wallet's own lokicash1... string, stored verbatim at
	// mint so the archive can keep the exact string that was issued when the
	// bill is destroyed (CashBillArchive.CashToken). Empty for every other
	// kind.
	//
	// Operator-side only: it is deliberately absent from api.App, so no REST
	// listing or NWC response ever carries it. Storing it grants no capability
	// that did not already exist — a cash bill's pairing key is
	// deterministically derivable from its app id (keys.GetCashPairingKey).
	CashToken  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastUsedAt *time.Time
	Kind       string `gorm:"not null;default:'standard'"`
	Metadata   datatypes.JSON

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
	// CashLineageRootID is the minted bill a bill's value descends from: a
	// minted bill's own id, a spin-off's source's root, a merge's first source's
	// root. Spin-offs are rate-limited per lineage.
	CashLineageRootID *uint `gorm:"index"`

	// The scheme-2 fields of a bill created by a zap transfer (NIP-CASH §Mint
	// Provenance), kept for the bill's life so its payload can be rebuilt; nil on
	// every other bill. Never the zap_target itself, only its sha256.
	ZapIssuedAt   *int64
	ZapExpiresAt  *int64
	ZapTargetHash *string `gorm:"size:64"`
	ZapRecipient  *string `gorm:"size:80"`
	ZapSender     *string `gorm:"size:80"`

	// CircleFeesPpm is a circle_wallet's forwarding-fee rate, fixed when the
	// member joined (NIP-CW §Fees): a host changing the Hub's rate later
	// affects only new members. Nil on wallets created before the column
	// existed, which keep paying the Hub's current rate.
	CircleFeesPpm *int
	// CircleMaxReceiveMloki is a circle_wallet's receive cap per budget period
	// (max_receive), as resolved at its join. Nil on every other kind.
	CircleMaxReceiveMloki *int64
	// CircleBudgetAnchorAt is when a circle_wallet's budget periods start
	// counting from (NIP-CW §Budget Renewal Floor): its creation, unix seconds.
	CircleBudgetAnchorAt *int64
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
	// RedeemFeeBaseMloki is the flat part of the same fee (0 = none), added to
	// the per-million part rather than replacing it.
	//
	// It exists because routing cost is mostly FIXED while RedeemFeePpm is purely
	// proportional, so a purely proportional fee cannot cover a small slice's own
	// redemption: at 0.1% a 1,000-mloki slice earns the Hub 1 mloki against a real
	// route that routinely costs hundreds, and the difference came out of the Hub's
	// own balance. Lightning's own forwarding fees are base + ppm for exactly this
	// reason, and so is every custodial service that survives doing this.
	//
	// Sized to cover a typical route, this makes the fee the Hub withholds from a
	// slice enough to pay for delivering it, which is what lets the payment's fee
	// cap be set to the quoted fee (see SendPaymentSync) and therefore what keeps
	// payout + cap within the slice itself.
	RedeemFeeBaseMloki int64
	// SpentRetentionSecs is how long after a bill is destroyed this Hub keeps
	// answering cash_status for it with a "spent" tombstone instead of going
	// silent. Measured from the spend, not from the bill's expiry, so a
	// never-expiring bill is covered too.
	//
	// It exists because silence alone cannot be told apart from a Hub that is
	// slow or unreachable, which forces every client to pick a wrong answer:
	// "retryable" makes a genuinely spent bill retry forever, "gone" tells
	// someone their funds are lost when the Hub is merely down.
	//
	// 0 disables the tombstone entirely — the bill is archived and the Hub
	// goes silent immediately, which is the behaviour that predates this
	// field. Past the deadline the Hub returns to silence either way, so the
	// tombstone is never wrong, only absent for old bills.
	SpentRetentionSecs int
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
	// incremented atomically by every in-place cash_transfer against this row
	// — pinned in each atomic update's WHERE clause so two concurrent
	// operations on the same slice can never both succeed (see
	// AppsService.ReassignCashSliceIdentity/ClaimCashSlice). It is not
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
	// RedeemFeeBaseMloki is this slice's own flat fee component, snapshotted and
	// inherited under exactly the same rules as RedeemFeePpm above: fixed at
	// creation, never rewritten by an identity reassignment, and carried unchanged
	// onto a split-off or consolidated wallet. The two are one quoted price and
	// must travel together, or a slice could be split into pieces that each pay a
	// different share of the same route.
	RedeemFeeBaseMloki int64
	// SpunOffToWalletAppID is set (alongside ClaimedAt) when this slice's
	// entire value was moved into a brand-new dedicated cash_wallet rather
	// than redeemed via a real Lightning payment — see
	// AppsService.SetCashSliceSplitTarget. Purely informational: every atomic
	// guard elsewhere already treats ClaimedAt != nil as terminal regardless
	// of which mechanism set it, so this column is never read by any guard,
	// only by callers (e.g. cash_status) that want to explain *why* a
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
	// RedeemProofID is the claim proof consumed by the cash_redeem whose payout
	// is in flight, "" for a cash-mode slice. A payout that definitely fails
	// releases the claim and forgets this proof in one transaction.
	RedeemProofID string `gorm:"size:64"`
	// RedeemCooldownUntil is set when a payout failed only after cash_redeem had
	// answered: until then another redeem of the slice is RATE_LIMITED.
	RedeemCooldownUntil *time.Time
	// EndedAs is the terminal state the claim that ended this slice gave it:
	// redeemed, transferred or consolidated ("" while live, or for a slice a
	// sweep ends). It decides the tombstone reason of the bill it destroys.
	EndedAs   string `gorm:"size:12"`
	CreatedAt time.Time
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
	Policy         string `gorm:"index"` // queried every tick by GetFollowSetCircleIdentities
	ProviderPubkey string
	// FollowSetD is the set's `d` identifier, set only for the follow_set
	// policy. ProviderPubkey and FollowSetD together are the set's address.
	FollowSetD string
	// LastFollowSetEvent is the newest version of the set this hub has read,
	// as JSON, so a restart can't roll membership back to an older version
	// relays still serve. Follow_set policy only.
	LastFollowSetEvent *string `json:"-"`
}

// FollowSetAddress is the NIP-01 address of a follow_set identity's set.
func (identity *CircleIdentity) FollowSetAddress() string {
	return fmt.Sprintf("%d:%s:%s", KindFollowSet, identity.ProviderPubkey, identity.FollowSetD)
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
	// MaxExpSecs is the ceiling on a wallet's lifetime, 0 = none; DefaultExpSecs
	// is the lifetime a join that names none gets, 0 = never. A default above a
	// non-zero ceiling, or 0 under one, is refused (NIP-CW §Data Model).
	MaxExpSecs     int
	DefaultExpSecs int
	FeesPpm        int
	// PerWalletMaxMloki caps a caller's requested max_amount per issued wallet
	// (required positive — mirrors CashHubConfig.PerWalletMaxMloki).
	PerWalletMaxMloki int
	// PerWalletMaxReceiveMloki caps a caller's requested max_receive.
	PerWalletMaxReceiveMloki int64
	// HoldingsCeilingMloki is the most the circle's Active wallets may hold
	// together, their open invoices included (NIP-CW §Own Funds and Caps).
	HoldingsCeilingMloki int64
	// FeeReservePpm and FeeReserveMinMloki are the routing-fee reserve rule a
	// member's Lightning payment holds back and may spend: the larger of the
	// minimum and amount × ppm / 1,000,000, rounded down.
	FeeReservePpm      int
	FeeReserveMinMloki int64
	// OutboundCeilingMloki optionally bounds what the circle's wallets together
	// send as external traffic in one OutboundPeriod of the Hub's own (daily to
	// yearly, from the Hub's creation); 0 is no ceiling.
	OutboundCeilingMloki int64
	OutboundPeriod       string
	// MinBudgetRenewal is the shortest (tightest) renewal period a caller may
	// request for their wallet's budget_renewal — protects the hub from
	// members resetting their spend cap too often. A request is rejected when
	// its constants.BudgetRenewalRank is tighter (lower) than this floor's
	// rank (e.g. floor "monthly" allows "monthly"/"yearly"/"never", rejects
	// "daily"/"weekly").
	MinBudgetRenewal string
	// AcceptingJoins is false once the host closes the circle to new members:
	// every join is then RESTRICTED (NIP-CW §The Circle Wallet Hub Connection).
	// NULL is the default, open.
	AcceptingJoins *bool
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

// CircleJoinRecord is a join's record (NIP-CW §Lost Join Reply): what a lookup by
// (signer, connection_pubkey) answers with. Kept while the wallet exists and at
// least until the join's deadline plus 6 minutes; no foreign key, since it
// outlives the wallet.
type CircleJoinRecord struct {
	ID               uint   `gorm:"primaryKey"`
	CircleHubAppID   uint   `gorm:"not null;index"`
	HubWalletPubkey  string `gorm:"size:64;not null"`
	HubConnPubkey    string `gorm:"size:64;not null"`
	AuthorizerPubkey string `gorm:"size:64;not null;uniqueIndex:idx_circle_join_record_key,priority:1"`
	ConnectionPubkey string `gorm:"size:64;not null;uniqueIndex:idx_circle_join_record_key,priority:2"`
	ProofCreatedAt   int64  `gorm:"not null"`
	Deadline         int64  `gorm:"not null;index"`
	EncryptedDetails string `gorm:"type:text;not null"`
	WalletAppID      uint   `gorm:"not null;index"`
}

// PrivateServedNonce is the nonce of an envelope this hub served on the private
// transport (NIP-CASH §Serving an Envelope, E3), kept until its not_after passes
// so a replay is dropped even across a restart.
type PrivateServedNonce struct {
	Nonce    string `gorm:"primaryKey;size:64"`
	NotAfter int64  `gorm:"not null;index"`
}

// CashConsumedProof marks a claim proof (kind 23198) as used, so a captured proof
// cannot authorize a second redeem, transfer or consolidate (NIP-CASH §Claim
// Proofs). Kept until well after the proof could pass the freshness check
// (nipcash.ClaimRetainUntil); pruned an hour after its created_at.
type CashConsumedProof struct {
	EventID        string `gorm:"primaryKey;size:64"`
	ProofCreatedAt int64  `gorm:"not null;index"`
	ConsumedAt     int64  `gorm:"not null"`
	Method         string `gorm:"size:24;not null"`
}

// CashUsedRequestID is the permanent used mark of a request_id (NIP-CASH
// §request_id): set once a call is authorized and never removed, so a reused id
// is refused REQUEST_REUSED forever.
type CashUsedRequestID struct {
	RequestID string `gorm:"primaryKey;size:64"`
	Method    string `gorm:"size:24;not null"`
	UsedAt    int64  `gorm:"not null"`
}

// CashRequestRecord is the record of a committed bill-creating call (NIP-CASH
// §The Record), written in the call's commit transaction. It holds what a lookup
// needs to rebuild the call's answer and never a secret, a token or a zap target.
type CashRequestRecord struct {
	ID                 uint    `gorm:"primaryKey"`
	RequestID          string  `gorm:"size:64;not null;uniqueIndex"`
	Method             string  `gorm:"size:24;not null"`
	OrderID            *uint   // cash_order: its order row
	WithdrawalID       *uint   // cash_withdraw: its withdrawal row
	HubAppID           uint    `gorm:"not null"` // no FK: the Hub cannot be deleted while a record names it
	TargetWalletPubkey string  `gorm:"size:64;not null;index"`
	TargetConnPubkey   string  `gorm:"size:64;not null"`
	AuthorizerKind     string  `gorm:"size:12;not null"` // pubkey | commitment | client
	Authorizer         string  `gorm:"size:64;not null"`
	Deadline           int64   `gorm:"not null"`
	DeliveryPubkey     *string `gorm:"size:64"` // inner-layer key; nil means the token is in the clear
	ResultMeta         string  `gorm:"type:text;not null"`
	CommittedAt        int64   `gorm:"not null"`
	DropAfter          *int64  `gorm:"index"` // set once every created bill is destroyed
}

// Roles of a record's bills.
const (
	CashRecordBillMinted    = "minted"
	CashRecordBillCarved    = "carved"
	CashRecordBillRemainder = "remainder"
	CashRecordBillSingle    = "single"
	CashRecordBillMerged    = "merged"
)

// CashRequestRecordBill is one bill a recorded call created, with what a lookup
// needs to rebuild its token (the connection secret is re-derived from
// WalletAppID; nothing secret is stored).
type CashRequestRecordBill struct {
	ID               uint              `gorm:"primaryKey"`
	RecordID         uint              `gorm:"not null;index"`
	Record           CashRequestRecord `gorm:"foreignKey:RecordID;constraint:OnDelete:CASCADE"`
	Ordinal          int16             `gorm:"not null"`
	Role             string            `gorm:"size:12;not null"`
	WalletAppID      uint              `gorm:"not null;index"` // no FK: the record outlives the bill
	WalletPubkey     string            `gorm:"size:64;not null"`
	RelayHints       string            `gorm:"type:text;not null"` // JSON array, exactly as encoded
	IdentityRequired *int16
	HubGroup         *string `gorm:"size:8"`
	AmountMillis     int64   `gorm:"not null"`
	Scheme           int16   `gorm:"not null"`
	Signature        string  `gorm:"size:130;not null"`
	ZapIssuedAt      *int64
	ZapExpiresAt     *int64
	ZapTargetHash    *string `gorm:"size:64"`
	ZapRecipient     *string `gorm:"size:80"`
	ZapSender        *string `gorm:"size:80"`
}

// CashHorizon holds the single horizon H (NIP-CASH §Deadlines) in its only row
// (ID 1). H never decreases, so the stored value is the largest ever used.
type CashHorizon struct {
	ID        int16 `gorm:"primaryKey"`
	HSecs     int64 `gorm:"not null"`
	UpdatedAt int64
}

// TableName is cash_horizon: one row, not a collection.
func (CashHorizon) TableName() string { return "cash_horizon" }

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
	// CashSliceStatusTransferred and CashSliceStatusConsolidated are the two
	// ways a slice's value moves into another bill, as a tombstone reason
	// tells them apart; the operator's listing groups both under split.
	CashSliceStatusTransferred  = "transferred"
	CashSliceStatusConsolidated = "consolidated"
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
// No foreign key to any App row: every app this record names is already gone by
// the time it exists, so a constraint could only ever be a liability. Retained forever — this is the
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
	// CashToken is the bill's own lokicash1... string, copied verbatim off the
	// App row (db.App.CashToken) that held it in life, for the operator's
	// audit trail — matching a token from a support ticket or an offline
	// record against the archive needs the literal string that was issued.
	//
	// Verbatim matters: the token embeds the relay hints in effect at mint
	// time, so re-deriving one later could produce a different string from the
	// one its holder actually has.
	//
	// Stored, never returned over NWC. The pairing secret inside it is
	// derivable from the app id only with the node's key material
	// (keys.GetCashPairingKey), so a database-only reader does learn it — but
	// it proves possession of the bill and nothing more: every method that moves
	// value or reads a slice also needs a slice proof or the cash secret, and
	// only the cash secret's hash is stored. NIP-CASH §Archival on Deletion
	// asks operators to treat a displayed token as live for the retention window
	// and as a record after, which is how the operator's bills list shows it.
	CashToken string
	// ConnectionPubkey is the bill's App.AppPubkey: the public half of the
	// connection secret inside its token, and the only key whose proof earns a
	// tombstone answer. Stored so that check is a comparison, costing the same
	// as the miss for a pubkey the Hub never served. Empty on rows that predate
	// the column, which then get no tombstone.
	ConnectionPubkey string
	MintedAt         time.Time
	// EndedAt is when the bill was deleted. Second column of the composite
	// index so a per-hub listing is index-ordered rather than filesorting a
	// forever-growing table.
	EndedAt time.Time `gorm:"not null;index:idx_cash_bill_archive_hub_ended,priority:2"`

	// RetainedUntil is EndedAt plus the Hub's SpentRetentionSecs at the moment
	// this row was written: the instant past which the Hub stops answering
	// cash_status about this bill. NULL means no tombstone applies at all
	// (retention disabled, or a row predating this column).
	//
	// Denormalised so the two paths that ask "which destroyed bills are still
	// answerable" are single indexed queries instead of a walk. They used to
	// pluck every archive row and then issue two more queries per row — 2N+1
	// statements for startup and for every five-minute sweep, which at a
	// million archived bills is two million statements and, on postgres,
	// minutes of work per tick. Same reasoning as CashSliceArchive's
	// denormalised wallet fields.
	//
	// It is a cache of policy, not a replacement for it: a Hub that changes
	// SpentRetentionSecs must have that apply to bills it already destroyed, so
	// apps.UpdateCashHubConfig recomputes this column for the Hub's rows. The
	// live semantics are unchanged from when it was computed per read.
	RetainedUntil *time.Time `gorm:"index"`
	ExpiresAt     *time.Time
	// Outcome is one of the CashBillOutcome* values.
	Outcome string `gorm:"not null;index"`
	// Reason is the tombstone reason (NIP-CASH §Answering About a Destroyed
	// Bill): the terminal state of the slice whose ending destroyed the bill, one
	// of redeemed, transferred, consolidated, expired and reclaimed. NULL for a
	// written-off bill, whose tombstone carries no reason.
	Reason *string `gorm:"size:12"`
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
	ID uint
	// AppId/Scope carry a composite index because every NWC request reads this
	// table twice — permissionsService.HasPermission filters on both, and
	// GetPermittedMethods on app_id alone, which uses the same index as a
	// prefix. The foreign key below is NOT an index: neither postgres nor
	// sqlite indexes the referencing side of a constraint, so without this
	// every request scanned the whole table, as did the app_permissions join in
	// queries.GetCircleCommitmentMloki that runs inside the advisory lock
	// admitting a circle member.
	AppId         uint   `validate:"required" gorm:"index:idx_app_permissions_app_scope,priority:1"`
	App           App    `gorm:"constraint:OnDelete:CASCADE;"`
	Scope         string `validate:"required" gorm:"index:idx_app_permissions_app_scope,priority:2"`
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
	AppId           *uint `gorm:"index:idx_transactions_app_type_state,priority:1;index:idx_transactions_app_period,priority:1"`
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

	// PaymentKind is a circle wallet payment's kind (NIP-CW §Own Funds and
	// Caps, 1 same circle … 4 Lightning) from the wallet's own side; 0 on a row
	// no circle wallet is party to. Set when an outgoing payment starts and when
	// an incoming one settles.
	PaymentKind int `gorm:"index:idx_transactions_app_period,priority:3"`
	// CountsSend marks an outgoing row of an Active circle wallet that counts
	// on its send cap; PeriodStart is the wallet's budget period (unix) the row
	// counts in: when the payment started, or when an incoming one settled.
	CountsSend  bool
	PeriodStart int64 `gorm:"index:idx_transactions_app_period,priority:2"`
	// OutboundPeriodStart is the Hub period (unix) an external payment of a
	// circle wallet counts in on the circle's outbound ceiling; 0 when it does
	// not count.
	OutboundPeriodStart int64
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
