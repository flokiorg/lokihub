package manager

import (
	"errors"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/lsps/persist"
	"gorm.io/gorm"
)

// LSPManager handles the business logic for LSP persistence and management
type LSPManager struct {
	db *gorm.DB
}

func NewLSPManager(db *gorm.DB) *LSPManager {
	m := &LSPManager{db: db}
	_ = db.AutoMigrate(&persist.LSP{})
	_ = m.CleanupInvalidLSPs()
	return m
}

// CleanupInvalidLSPs removes LSPs with empty pubkeys or hosts
func (m *LSPManager) CleanupInvalidLSPs() error {
	// Delete where pubkey is empty string
	if err := m.db.Where("pubkey = ?", "").Delete(&persist.LSP{}).Error; err != nil {
		return err
	}
	// Delete where host is empty string (if allowed by schema, but strictly should be valid)
	if err := m.db.Where("host = ?", "").Delete(&persist.LSP{}).Error; err != nil {
		return err
	}
	return nil
}

// ListLSPs returns all LSPs from the database
func (m *LSPManager) ListLSPs() ([]persist.LSP, error) {
	var lsps []persist.LSP
	if err := m.db.Find(&lsps).Error; err != nil {
		return nil, err
	}
	return lsps, nil
}

// AddLSP adds a new LSP (Custom or Community)
func (m *LSPManager) AddLSP(name, pubkey, host string, active bool, isCommunity bool) (*persist.LSP, error) {
	pubkey = strings.ToLower(pubkey)

	// Check if exists
	// Check if exists
	var count int64
	if err := m.db.Model(&persist.LSP{}).Where("pubkey = ?", pubkey).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, errors.New("LSP with this pubkey already exists")
	}

	lsp := persist.LSP{
		Pubkey:      pubkey,
		Host:        host,
		Name:        name,
		IsActive:    active,
		IsCommunity: isCommunity,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := m.db.Create(&lsp).Error; err != nil {
		return nil, err
	}
	return &lsp, nil
}

// ToggleLSP updates the active status of an LSP
func (m *LSPManager) ToggleLSP(pubkey string, active bool) error {
	pubkey = strings.ToLower(pubkey)
	result := m.db.Model(&persist.LSP{}).Where("pubkey = ?", pubkey).Update("is_active", active)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("LSP not found")
	}
	return nil
}

// DeleteCustomLSP removes a custom LSP. System LSPs cannot be deleted.
func (m *LSPManager) DeleteCustomLSP(pubkey string) error {
	pubkey = strings.ToLower(pubkey)
	var lsp persist.LSP
	if err := m.db.First(&lsp, "pubkey = ?", pubkey).Error; err != nil {
		return err
	}

	if lsp.IsCommunity {
		return errors.New("cannot delete system/community LSP")
	}

	return m.db.Delete(&lsp).Error
}

// SyncSystemLSPs synchronizes the hardcoded community LSPs with the database.
// It effectively "Upserts" them, adding new ones but PRESERVING user's active/inactive choice on existing ones.
type CommunityLSPInput struct {
	Name        string
	Description string
	Pubkey      string
	Host        string
}

func (m *LSPManager) SyncSystemLSPs(inputs []CommunityLSPInput) error {
	for _, input := range inputs {
		// Filter out invalid inputs (empty pubkey or host)
		if input.Pubkey == "" || input.Host == "" {
			continue
		}

		input.Pubkey = strings.ToLower(input.Pubkey)

		// Try to find existing
		var existing persist.LSP
		err := m.db.First(&existing, "pubkey = ?", input.Pubkey).Error

		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Create new
			newLSP := persist.LSP{
				Pubkey:      input.Pubkey,
				Host:        input.Host,
				Name:        input.Name,
				Description: input.Description,
				IsActive:    false, // Default to inactive for new ones
				IsCommunity: true,
				CreatedAt:   time.Now(),
				UpdatedAt:   time.Now(),
			}
			if err := m.db.Create(&newLSP).Error; err != nil {
				return err
			}
		} else if err == nil {
			// Update properties (Host/Name/Description) but NOT IsActive
			// Also ensure IsCommunity is true
			existing.Host = input.Host
			existing.Name = input.Name
			existing.Description = input.Description
			existing.IsCommunity = true
			existing.UpdatedAt = time.Now()

			// We can use Updates for partial update
			if err := m.db.Model(&existing).Where("pubkey = ?", existing.Pubkey).Updates(map[string]interface{}{
				"host":         input.Host,
				"name":         input.Name,
				"description":  input.Description,
				"is_community": true,
				"updated_at":   time.Now(),
			}).Error; err != nil {
				return err
			}
		} else {
			return err
		}
	}
	return nil
}
