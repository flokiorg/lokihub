package apps

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

var (
	// ErrInvalidIdentityAuthorityPubkey is returned when a pubkey is not a
	// 64-character lowercase hex string.
	ErrInvalidIdentityAuthorityPubkey = errors.New("pubkey must be a 64-character hex string")
	// ErrDuplicateIdentityAuthorityPubkey is returned by
	// IdentityAuthorityManager.Add when the pubkey is already registered.
	ErrDuplicateIdentityAuthorityPubkey = errors.New("an Identity Authority with this pubkey already exists")
	// ErrIdentityAuthorityNotFound is returned by
	// IdentityAuthorityManager.Delete when the pubkey is not registered.
	ErrIdentityAuthorityNotFound = errors.New("identity authority not found")
)

// IdentityAuthority is a Nostr identity the hub owner trusts to attest
// connection_key ownership claims (Kind 35522 events) during a JIT wallet's
// claim_funds flow. The registry is global/instance-wide, not scoped
// per jit_hub, mirroring the LSP registry's scope.
type IdentityAuthority struct {
	Pubkey    string    `gorm:"primaryKey" json:"pubkey"`
	Name      string    `json:"name"`
	RelayURLs string    `json:"relayUrls"` // comma-joined, same convention as config.GetRelayUrls()
	CreatedAt time.Time `json:"createdAt"`
}

// TableName overrides the table name to 'identity_authorities'.
func (IdentityAuthority) TableName() string {
	return "identity_authorities"
}

// IdentityAuthorityManager persists and enforces the registry of trusted
// Identity Authorities.
type IdentityAuthorityManager struct {
	db *gorm.DB
}

func NewIdentityAuthorityManager(db *gorm.DB) *IdentityAuthorityManager {
	m := &IdentityAuthorityManager{db: db}
	_ = db.AutoMigrate(&IdentityAuthority{})
	return m
}

// List returns every registered Identity Authority, ordered by creation time.
func (m *IdentityAuthorityManager) List() ([]IdentityAuthority, error) {
	var authorities []IdentityAuthority
	if err := m.db.Order("created_at asc").Find(&authorities).Error; err != nil {
		return nil, err
	}
	return authorities, nil
}

// Add registers a new Identity Authority. pubkey must be a 64-character lowercase
// hex nostr pubkey; relayURLs is stored as informational/documentation metadata only.
func (m *IdentityAuthorityManager) Add(pubkey, name string, relayURLs []string) (*IdentityAuthority, error) {
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))
	if decoded, err := hex.DecodeString(pubkey); err != nil || len(decoded) != 32 {
		return nil, ErrInvalidIdentityAuthorityPubkey
	}

	var count int64
	if err := m.db.Model(&IdentityAuthority{}).Where("pubkey = ?", pubkey).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, ErrDuplicateIdentityAuthorityPubkey
	}

	authority := &IdentityAuthority{
		Pubkey:    pubkey,
		Name:      name,
		RelayURLs: strings.Join(relayURLs, ","),
		CreatedAt: time.Now(),
	}
	if err := m.db.Create(authority).Error; err != nil {
		return nil, err
	}
	return authority, nil
}

// Delete removes an Identity Authority from the registry.
func (m *IdentityAuthorityManager) Delete(pubkey string) error {
	result := m.db.Where("pubkey = ?", strings.ToLower(strings.TrimSpace(pubkey))).Delete(&IdentityAuthority{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrIdentityAuthorityNotFound
	}
	return nil
}

// IsTrusted reports whether pubkey is a registered Identity Authority. This is
// the enforcement primitive: create_jit_wallet_controller.go calls this before
// accepting an ia_pubkey for a connection_key-mode wallet.
func (m *IdentityAuthorityManager) IsTrusted(pubkey string) (bool, error) {
	var count int64
	err := m.db.Model(&IdentityAuthority{}).
		Where("pubkey = ?", strings.ToLower(strings.TrimSpace(pubkey))).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("failed to check Identity Authority trust: %w", err)
	}
	return count > 0, nil
}
