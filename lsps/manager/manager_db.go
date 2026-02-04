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
	_ = db.AutoMigrate(&persist.LSPS1Order{})
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

// UpdateLSPNostrPubkey updates the nostr pubkey for an LSP
func (m *LSPManager) UpdateLSPNostrPubkey(pubkey, nostrPubkey string) error {
	pubkey = strings.ToLower(pubkey)
	return m.db.Model(&persist.LSP{}).Where("pubkey = ?", pubkey).Update("nostr_pubkey", nostrPubkey).Error
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
	Website     string
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
				Website:     input.Website,
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
			// Update properties (Host/Name/Description/Website) but NOT IsActive
			// Also ensure IsCommunity is true
			existing.Host = input.Host
			existing.Name = input.Name
			existing.Website = input.Website
			existing.Description = input.Description
			existing.IsCommunity = true
			existing.UpdatedAt = time.Now()

			// We can use Updates for partial update
			if err := m.db.Model(&existing).Where("pubkey = ?", existing.Pubkey).Updates(map[string]interface{}{
				"host":         input.Host,
				"name":         input.Name,
				"website":      input.Website,
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

// LSPS1 Order Persistence

// CreateOrder saves a new LSPS1 order
func (m *LSPManager) CreateOrder(order *persist.LSPS1Order) error {
	return m.db.Create(order).Error
}

// GetOrder fetches an order by ID
func (m *LSPManager) GetOrder(orderID string) (*persist.LSPS1Order, error) {
	var order persist.LSPS1Order
	if err := m.db.First(&order, "order_id = ?", orderID).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

// UpdateOrderState updates the state of an existing order
func (m *LSPManager) UpdateOrderState(orderID, state string) error {
	return m.db.Model(&persist.LSPS1Order{}).Where("order_id = ?", orderID).Update("state", state).Error
}

// ListAllOrders returns all orders (history)
func (m *LSPManager) ListAllOrders() ([]persist.LSPS1Order, error) {
	var orders []persist.LSPS1Order
	// Sort by created_at desc
	err := m.db.Order("created_at desc").Find(&orders).Error
	return orders, err
}

// ListPendingOrders returns orders that are not in a terminal state
func (m *LSPManager) ListPendingOrders() ([]persist.LSPS1Order, error) {
	var orders []persist.LSPS1Order
	// Define terminal states that don't need tracking.
	// We might want to keep checking "CREATED" logic.
	// Typically, terminal states are: "COMPLETED", "FAILED", "CANCELLED", "CLOSED" (from previous logic)
	// We filter where state NOT IN (...)
	terminalStates := []string{"COMPLETED", "FAILED", "CANCELLED", "CLOSED"}
	if err := m.db.Where("state NOT IN ?", terminalStates).Find(&orders).Error; err != nil {
		return nil, err
	}
	return orders, nil
}

// DeleteOrder removes an order (used if we want to clean up old ones or purely temporary)
func (m *LSPManager) DeleteOrder(orderID string) error {
	return m.db.Delete(&persist.LSPS1Order{}, "order_id = ?", orderID).Error
}
