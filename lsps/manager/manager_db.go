package manager

import (
	"errors"
	"fmt"
	"strings"

	"github.com/flokiorg/lokihub/lsps/persist"
	"gorm.io/gorm"
)

// LSPManager handles the business logic for LSP persistence and management
type LSPManager struct {
	db *gorm.DB
}

func NewLSPManager(db *gorm.DB) *LSPManager {
	m := &LSPManager{db: db}
	// The catalogue table is recreated, never migrated (LP-14): a table from
	// before NIP-LSP (services.json LSPs, pubkey@host entries) is dropped and the
	// user picks trusted LSPs again from the announcements.
	if db.Migrator().HasTable(&persist.LSP{}) && !db.Migrator().HasColumn(&persist.LSP{}, "node_key") {
		_ = db.Migrator().DropTable(&persist.LSP{})
	}
	_ = db.AutoMigrate(&persist.LSP{})
	_ = db.AutoMigrate(&persist.LSPS1Order{})
	return m
}

// DB is the catalogue's database.
func (m *LSPManager) DB() *gorm.DB { return m.db }

// ListLSPs returns the whole catalogue.
func (m *LSPManager) ListLSPs() ([]persist.LSP, error) {
	var lsps []persist.LSP
	if err := m.db.Order("name, node_key").Find(&lsps).Error; err != nil {
		return nil, err
	}
	return lsps, nil
}

// ListTrustedLSPs returns the LSPs the user trusts.
func (m *LSPManager) ListTrustedLSPs() ([]persist.LSP, error) {
	var lsps []persist.LSP
	if err := m.db.Where("trusted = ?", true).Order("name, node_key").Find(&lsps).Error; err != nil {
		return nil, err
	}
	return lsps, nil
}

// GetLSP returns one LSP by node key (x-only) or node id (66 hex).
func (m *LSPManager) GetLSP(key string) (*persist.LSP, error) {
	key = NodeKeyOf(key)
	var lsp persist.LSP
	if err := m.db.First(&lsp, "node_key = ?", key).Error; err != nil {
		return nil, err
	}
	return &lsp, nil
}

// SetTrusted records the user's choice for an LSP.
func (m *LSPManager) SetTrusted(key string, trusted bool) error {
	result := m.db.Model(&persist.LSP{}).Where("node_key = ?", NodeKeyOf(key)).Update("trusted", trusted)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("LSP not found")
	}
	return nil
}

// UpdateLSPNostrPubkey stores the LSPS0 notification pubkey read live (LP-18).
func (m *LSPManager) UpdateLSPNostrPubkey(key, nostrPubkey string) error {
	return m.db.Model(&persist.LSP{}).Where("node_key = ?", NodeKeyOf(key)).Update("nostr_pubkey", nostrPubkey).Error
}

// NodeKeyOf is the x-only node key of a node key or a 66-hex node id.
func NodeKeyOf(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if len(key) == 66 && (strings.HasPrefix(key, "02") || strings.HasPrefix(key, "03")) {
		return key[2:]
	}
	return key
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

// UpdateOrderStateIfPriority atomically reads the current state and applies the update only
// if allow(currentState) returns true. lspPubkey MUST match the order's own LSPPubkey — this is
// the single authoritative ownership guard every caller goes through (HTTP webhook and Nostr
// notification paths both eventually call this), closing the gap where a trusted-but-wrong LSP
// could otherwise forge a state change for an order it doesn't own (docs/lokihub-notifications.md's
// own "Known gap" section). api.UpdateLSPS1OrderState's separate pre-check is a legitimate
// fast-fail on top of this, not a substitute for it — this check is the one every path actually
// depends on. Returns (true, nil) when applied, (false, nil) when skipped by allow, and a non-nil
// error for a genuine ownership mismatch.
func (m *LSPManager) UpdateOrderStateIfPriority(orderID, newState, lspPubkey string, allow func(current string) bool) (bool, error) {
	applied := false
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var order persist.LSPS1Order
		if err := tx.First(&order, "order_id = ?", orderID).Error; err != nil {
			return err
		}
		if order.LSPPubkey != lspPubkey {
			return fmt.Errorf("lsp pubkey %q does not match the LSP that created order %s", lspPubkey, orderID)
		}
		if !allow(order.State) {
			return nil
		}
		applied = true
		return tx.Model(&persist.LSPS1Order{}).Where("order_id = ?", orderID).Update("state", newState).Error
	})
	return applied, err
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
	terminalStates := []string{"COMPLETED", "FAILED", "CANCELLED", "CLOSED", "EXPIRED"}
	if err := m.db.Where("state NOT IN ?", terminalStates).Find(&orders).Error; err != nil {
		return nil, err
	}
	return orders, nil
}

// DeleteOrder removes an order (used if we want to clean up old ones or purely temporary)
func (m *LSPManager) DeleteOrder(orderID string) error {
	return m.db.Delete(&persist.LSPS1Order{}, "order_id = ?", orderID).Error
}
