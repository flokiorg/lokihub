package db

import (
	"fmt"

	"gorm.io/gorm"
)

// Lock namespaces for pg_advisory_xact_lock's two-key form, a key space apart
// from the single-key app locks (LockApp).
const (
	// LockNsConnKey serialises every check-and-claim of a connection key on the
	// node: a join's step 9 and a rotation's acceptance (NIP-CW §Recovering a
	// Lost Connection: "checked and accepted as one atomic step with any join's
	// step 9").
	LockNsConnKey int32 = 0x434b
	// LockNsOrder serialises a Cash Hub's order placements and payments
	// against its open-order ceiling (NIP-CO §Limits), keyed by the Hub's id.
	LockNsOrder int32 = 0x4f52
	// LockNsRequest serialises a call's record commit with a lookup's final
	// read of that request_id (NIP-CASH §Lost Replies: "unknown" means never),
	// keyed by a hash of the request_id.
	LockNsRequest int32 = 0x5251
	// LockNsLiveBills serialises a Cash Hub's capped spin-offs and merges
	// against its live-bill cap, keyed by the Hub's id.
	LockNsLiveBills int32 = 0x4c42
)

// LockApp takes the transaction-scoped lock of one app (a Hub or a wallet) on
// postgres. sqlite opens every write transaction with BEGIN IMMEDIATE, which
// already serialises them. Callers lock a Hub before any of its wallets, and
// wallets in ascending id order.
func LockApp(tx *gorm.DB, appID uint) error {
	if tx.Name() != "postgres" {
		return nil
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(appID)).Error; err != nil { //nolint:gosec // a row id, far below MaxInt64
		return fmt.Errorf("lock app %d: %w", appID, err)
	}
	return nil
}

// LockNamespace takes the transaction-scoped lock (ns, key) on postgres.
func LockNamespace(tx *gorm.DB, ns, key int32) error {
	if tx.Name() != "postgres" {
		return nil
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock($1, $2)", ns, key).Error; err != nil {
		return fmt.Errorf("lock namespace %d: %w", ns, err)
	}
	return nil
}
