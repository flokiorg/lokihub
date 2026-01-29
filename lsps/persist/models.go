package persist

import (
	"time"
)

// LSP represents a Lightning Service Provider in the database.
// This replaces the legacy blob storage in lsps_states.
type LSP struct {
	Pubkey      string    `gorm:"primaryKey" json:"pubkey"`
	Host        string    `gorm:"not null" json:"host"`
	Name        string    `json:"name"`
	Description string    `json:"description"`                      // Description of the LSP service
	NostrPubkey string    `json:"nostrPubkey"`                      // Notification pubkey for LSPS5
	IsActive    bool      `gorm:"default:false" json:"isActive"`    // Whether the user has selected/enabled this LSP
	IsCommunity bool      `gorm:"default:false" json:"isCommunity"` // True for LSPs from community config, False for user-added
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// TableName overrides the table name to 'lsps'
func (LSP) TableName() string {
	return "lsps"
}
