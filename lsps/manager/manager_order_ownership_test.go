package manager

import (
	"testing"
	"time"

	"github.com/flokiorg/lokihub/lsps/persist"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUpdateOrderStateIfPriority_LSPPubkeyMismatch_Rejected is the direct
// regression test for a real gap found by reviewing docs/lokihub-notifications.md's
// own "Known gap" section against the code: the Nostr LSPS5 notification path
// (lsps/nostr/listener.go -> service/lsps_events.go) called
// LiquidityManager.HandleOrderStateUpdate directly, which had no ownership
// check at all — any trusted LSP's signed event could change any order's
// state, not just orders that LSP actually created. The HTTP webhook path
// happened to be safe only because its own controller (api.UpdateLSPS1OrderState)
// added a separate pre-check before ever reaching this function. This proves
// the fix: the check now lives in the one function every path shares.
func TestUpdateOrderStateIfPriority_LSPPubkeyMismatch_Rejected(t *testing.T) {
	db := setupTestDB(t)
	manager := NewLSPManager(db)

	order := &persist.LSPS1Order{
		OrderID:   "order_mismatch_reject",
		LSPPubkey: "alice_pubkey",
		State:     "CREATED",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, manager.CreateOrder(order))

	// Mallory (a different, otherwise-trusted LSP) tries to change Alice's order.
	applied, err := manager.UpdateOrderStateIfPriority(order.OrderID, "COMPLETED", "mallory_pubkey",
		func(current string) bool { return true })
	require.Error(t, err, "a mismatched lsp pubkey must be rejected")
	assert.False(t, applied)

	fetched, err := manager.GetOrder(order.OrderID)
	require.NoError(t, err)
	assert.Equal(t, "CREATED", fetched.State, "the order's state must be unchanged after a rejected forgery attempt")
}

// TestUpdateOrderStateIfPriority_LSPPubkeyMatch_Applied confirms the ownership
// check doesn't false-positive-reject the legitimate case: the owning LSP can
// still update its own order exactly as before.
func TestUpdateOrderStateIfPriority_LSPPubkeyMatch_Applied(t *testing.T) {
	db := setupTestDB(t)
	manager := NewLSPManager(db)

	order := &persist.LSPS1Order{
		OrderID:   "order_match_applied",
		LSPPubkey: "alice_pubkey",
		State:     "CREATED",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, manager.CreateOrder(order))

	applied, err := manager.UpdateOrderStateIfPriority(order.OrderID, "COMPLETED", "alice_pubkey",
		func(current string) bool { return true })
	require.NoError(t, err)
	assert.True(t, applied)

	fetched, err := manager.GetOrder(order.OrderID)
	require.NoError(t, err)
	assert.Equal(t, "COMPLETED", fetched.State)
}

// TestHandleOrderStateUpdate_LSPPubkeyMismatch_StateNotChanged exercises the
// same fix one layer up, at HandleOrderStateUpdate — the exact function the
// Nostr listener's event consumer (service/lsps_events.go) calls with an
// lspPubkey it never previously validated against the order's own owner.
func TestHandleOrderStateUpdate_LSPPubkeyMismatch_StateNotChanged(t *testing.T) {
	db := setupTestDB(t)
	lspManager := NewLSPManager(db)

	order := &persist.LSPS1Order{
		OrderID:   "order_handle_update_mismatch",
		LSPPubkey: "alice_pubkey",
		State:     "CREATED",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, lspManager.CreateOrder(order))

	m := &LiquidityManager{cfg: &ManagerConfig{LSPManager: lspManager}}

	// Simulates a trusted-but-wrong LSP's forged Nostr notification reaching
	// HandleOrderStateUpdate directly, exactly as the Nostr path does.
	m.HandleOrderStateUpdate(order.OrderID, "COMPLETED", "mallory_pubkey")

	fetched, err := lspManager.GetOrder(order.OrderID)
	require.NoError(t, err)
	assert.Equal(t, "CREATED", fetched.State, "a forged notification from the wrong LSP must not change the order's state")
}
