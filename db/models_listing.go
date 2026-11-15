package db

// Offer listings (NIP-CASH §Listing Offers, §Hub Rules). The listing package
// owns these rows; nothing else writes them.

// AppKindCircleHubConn is a listed circle's own connection (NIP-CASH §Hub Rules:
// the same TLV 0, its own secret; NIP-CW §The Circle Wallet Hub Connection). It is
// a child of the circle_hub (ParentAppID, ParentKind ParentKindListing), shares the
// hub's WalletPubkey, holds its own AppPubkey (derived from its id with
// keys.GetCashPairingKey) and has no balance, budget or permissions: on the
// standard transport it answers get_info as the hub, and joins through it are
// served by the hub on the private transport. It publishes no NIP-47 info event:
// kind 13194 is replaceable per author, and the hub's own event, by the same
// wallet key, already covers it.
const AppKindCircleHubConn = "circle_hub_conn"

// ParentKindListing marks a circle_hub_conn row, so no query over a hub's
// circle_wallet or cash_wallet children (which filter on ParentKindCircle or
// ParentKindCash) ever counts it.
const ParentKindListing = "listing"

// Listing types, statuses and phases as stored.
const (
	ListingTypeCashOrder    = "cash_order"
	ListingTypeCircleWallet = "circle_wallet"

	ListingStatusOpen   = "open"
	ListingStatusPaused = "paused"
	ListingStatusClosed = "closed"

	// ListingPhaseListed: open or paused versions are published and kept fresh.
	ListingPhaseListed = "listed"
	// ListingPhaseClosing: the closed version is published and kept on every relay
	// that took an open one until DeleteDueAt.
	ListingPhaseClosing = "closing"
	// ListingPhaseDeleted: the NIP-09 deletion is published; nothing more is sent.
	ListingPhaseDeleted = "deleted"
)

// OfferListing is one listed offer: an Order connection or a circle, published as
// kind-31190 versions signed by the node identity. No column holds a secret (the
// listed string is rebuilt from the app rows) or an operator key.
type OfferListing struct {
	ID uint `gorm:"primaryKey"`
	// Type is cash_order or circle_wallet.
	Type string `gorm:"size:13;not null"`
	// HubAppID is the Cash Hub (cash_order) or the circle_hub (circle_wallet).
	HubAppID uint `gorm:"not null;index"`
	// ConnAppID is the listed connection's app: the Order connection, or the
	// circle's circle_hub_conn row. Zero once a circle's listing connection is
	// revoked after the deletion.
	ConnAppID uint `gorm:"not null;index"`
	// D is the listed string's TLV 0, the listing's d tag. One listing per address.
	D string `gorm:"size:64;not null;uniqueIndex"`

	Title    string `gorm:"size:160"`
	Content  string `gorm:"type:text"`
	Image    string `gorm:"size:2048"`
	Location string `gorm:"size:160"`
	// Languages and Geohashes are JSON arrays of strings.
	Languages string `gorm:"type:text"`
	Geohashes string `gorm:"type:text"`
	// Hints lists the cash_order hint tags from the current terms.
	Hints bool `gorm:"not null;default:false"`
	// FollowSetA adds the follow set's a tag (follow_set circles only), after the
	// host confirmed the set is public.
	FollowSetA     bool    `gorm:"not null;default:false"`
	OperatorPubkey *string `gorm:"size:64"`
	OperatorRelay  *string `gorm:"size:255"`

	Status string `gorm:"size:6;not null"`
	Phase  string `gorm:"size:8;not null;index"`

	// The last version published (whatever its status).
	LastCreatedAt int64
	LastEventID   string `gorm:"size:64"`
	// LastEventJSON is the last signed version, kept to build the closed version
	// from and to re-send it.
	LastEventJSON string `gorm:"type:text"`
	// The last open or paused version: its created_at and effective expiry
	// (min(expiration, created_at + 30 days)).
	LastOpenCreatedAt *int64
	LastOpenExpiresAt *int64
	// OpenRelays are the relays (JSON array) that accepted an open or paused
	// version; ClosedRelays those that accepted the closed one.
	OpenRelays   string `gorm:"type:text"`
	ClosedRelays string `gorm:"type:text"`

	// Due is when the scheduler next acts on this row: a republish, a retry or the
	// deletion. NextRetryAt and FailingSince drive the publish backoff.
	DueAt        int64 `gorm:"not null;default:0;index"`
	FailingSince *int64
	RetryDelay   int64 `gorm:"not null;default:0"`

	ClosedAt    *int64
	DeleteDueAt *int64
	DeletedAt   *int64

	RelaysOK          int
	RelaysTotal       int
	OperatorVerified  *bool
	OperatorCheckedAt *int64

	CreatedAt int64 `gorm:"autoCreateTime"`
	UpdatedAt int64 `gorm:"autoUpdateTime"`
}

// ListingNodeState is the node identity's listing-wide state: the relays of the
// last kind 10002 it published and that event's created_at (never post-dated, and
// strictly later than the one before). One row, ID 1.
type ListingNodeState struct {
	ID                 uint   `gorm:"primaryKey"`
	RelayListRelays    string `gorm:"type:text"`
	RelayListCreatedAt int64
}
