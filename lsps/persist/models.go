package persist

import (
	"time"
)

// LSP is one Lightning Service Provider of the catalogue, built from its NIP-LSP
// kind-10735 announcement (WP-26). The table is recreated, never migrated
// (LP-14): an LSP is trusted only when the user ticks it.
type LSP struct {
	// NodeKey is the announcement's pubkey, the node key x-only (64 hex).
	NodeKey string `gorm:"primaryKey" json:"nodeKey"`
	// Pubkey is the node id (66 hex, 02 or 03 + NodeKey) once known: from this
	// node's view of the graph, or the one a connection reached. Every LSPS call
	// and order uses it. Empty until resolved.
	Pubkey string `gorm:"index" json:"pubkey"`
	// Host is the address used to connect (host:port), onion first.
	Host string `json:"host"`

	// The newest announcement that passed every check (NIP-LSP §Client Behavior).
	Name        string `json:"name"` // title, as claimed
	Description string `json:"description"`
	Website     string `json:"website"`
	Image       string `json:"image"`
	Chain       string `json:"chain"`
	Network     string `json:"network"`
	Status      string `json:"status"` // open | paused | closed
	// Addrs, Protocols, LSPS1, LSPS2Fees, LSPS5, Languages and Relays are JSON.
	Addrs          string `gorm:"type:text" json:"-"`
	Protocols      string `gorm:"type:text" json:"-"`
	LSPS1          string `gorm:"type:text" json:"-"`
	LSPS2Fees      string `gorm:"type:text" json:"-"`
	LSPS5          string `gorm:"type:text" json:"-"`
	Languages      string `gorm:"type:text" json:"-"`
	Relays         string `gorm:"type:text" json:"-"`
	OperatorPubkey string `json:"operatorPubkey"`
	LastEventID    string `json:"lastEventId"`
	// LastEventJSON is the usable announcement, kept for the live check.
	LastEventJSON string `gorm:"type:text" json:"-"`
	// AnnouncedAt is the usable announcement's created_at; ExpiresAt its
	// expiration.
	AnnouncedAt int64 `json:"announcedAt"`
	ExpiresAt   int64 `json:"expiresAt"`
	// NewestSeenCreatedAt and NewestSeenID mark the newest announcement seen,
	// written even when it failed a check, so an older one is never used again.
	NewestSeenCreatedAt int64  `json:"newestSeenCreatedAt"`
	NewestSeenID        string `json:"-"`
	// Usable is false when the newest seen failed a check (InvalidReason says
	// which), or it expired.
	Usable        bool   `json:"usable"`
	InvalidReason string `json:"invalidReason"`

	// Graph facts from this node's own view of the network.
	InGraph        bool  `json:"inGraph"`
	GraphChannels  int   `json:"graphChannels"`
	GraphCapacity  int64 `json:"graphCapacity"`
	GraphCheckedAt int64 `json:"graphCheckedAt"`

	// Trusted is the user's choice, kept per node key (LP-12: none by default).
	Trusted bool `gorm:"default:false;index" json:"trusted"`
	// LastCheckAt and LastCheckResult are the last live check (JSON).
	LastCheckAt     int64  `json:"lastCheckAt"`
	LastCheckResult string `gorm:"type:text" json:"-"`

	// NostrPubkey is read live from LSPS0 get_info (LP-18), never announced.
	NostrPubkey string    `json:"nostrPubkey"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// TableName overrides the table name to 'lsps'
func (LSP) TableName() string {
	return "lsps"
}

// LSPSeenMark is the newest announcement seen from a node whose catalogue row
// was pruned: kept so a relay cannot re-feed an older version of it as new
// (NIP-LSP §Client Behavior, newest seen; R2-S3). Dropped once no version it
// covers can still be unexpired.
type LSPSeenMark struct {
	NodeKey   string `gorm:"primaryKey"`
	CreatedAt int64  `gorm:"not null;index"`
	EventID   string `gorm:"not null"`
}
