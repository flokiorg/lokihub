package manager

import (
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to open valid DB: %v", err)
	}
	return db
}

func TestLSPS1OrderPersistence(t *testing.T) {
	db := setupTestDB(t)
	manager := NewLSPManager(db)

	// Test 1: Create Order
	order := &persist.LSPS1Order{
		OrderID:        "order_123",
		LSPPubkey:      "pubkey_abc",
		State:          "CREATED",
		PaymentInvoice: "lnbc1...",
		FeeTotal:       1000,
		OrderTotal:     100000,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	err := manager.CreateOrder(order)
	assert.NoError(t, err)

	// Test 2: Get Order
	fetched, err := manager.GetOrder("order_123")
	assert.NoError(t, err)
	assert.Equal(t, order.OrderID, fetched.OrderID)
	assert.Equal(t, order.LSPPubkey, fetched.LSPPubkey)
	assert.Equal(t, order.State, fetched.State)
	assert.Equal(t, order.FeeTotal, fetched.FeeTotal)

	// Test 3: Update Order State
	err = manager.UpdateOrderState("order_123", "OPEN")
	assert.NoError(t, err)

	fetchedAfterUpdate, err := manager.GetOrder("order_123")
	assert.NoError(t, err)
	assert.Equal(t, "OPEN", fetchedAfterUpdate.State)

	// Test 4: List Pending Orders
	// Create a second order that is COMPLETED (terminal)
	completedOrder := &persist.LSPS1Order{
		OrderID:   "order_456",
		LSPPubkey: "pubkey_def",
		State:     "COMPLETED",
		CreatedAt: time.Now(),
	}
	err = manager.CreateOrder(completedOrder)
	assert.NoError(t, err)

	// List pending - should only return order_123 (OPEN is not terminal)
	pending, err := manager.ListPendingOrders()
	assert.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Equal(t, "order_123", pending[0].OrderID)

	// Update order_123 to FAILED (terminal)
	err = manager.UpdateOrderState("order_123", "FAILED")
	assert.NoError(t, err)

	// List pending - should return none
	pending, err = manager.ListPendingOrders()
	assert.NoError(t, err)
	assert.Len(t, pending, 0)

	// Test 5: Delete Order
	err = manager.DeleteOrder("order_123")
	assert.NoError(t, err)

	_, err = manager.GetOrder("order_123")
	assert.Error(t, err) // Should not find
}
