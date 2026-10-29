package apps

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// ErrSliceClaimed: the slice was claimed, transferred or removed by another call
// after this one read it. The commit rolls back (NIP-CASH: RESTRICTED, never paid
// twice).
var ErrSliceClaimed = errors.New("this slice was already claimed by another call")

// ErrIdentityHoldsSlice: an in-place transfer's new identity already holds a
// live slice on the wallet, committed by a concurrent call after this one
// decided its outcome. The commit rolls back (RESTRICTED).
var ErrIdentityHoldsSlice = errors.New("the new identity already holds a slice on this wallet")

// SliceRef names one slice exactly as a call read and authorized it: the row,
// the identity it was registered to and its version (TransferCount, which every
// in-place transfer advances).
type SliceRef struct {
	ClaimID       uint
	IdentityType  string
	IdentityValue string
	TransferCount int
}

// ClaimCashSliceTx claims one slice terminal inside tx, guarded on the exact
// identity and version the caller authorized and on claimed_at IS NULL, so a
// concurrent claim, transfer or removal makes it ErrSliceClaimed. spunOffTo, when
// set, records the bill the slice's value moved into; endedAs is the terminal
// state the claim gives the slice (db.CashSliceStatus*).
func ClaimCashSliceTx(tx *gorm.DB, ref SliceRef, spunOffTo *uint, endedAs string, now time.Time) error {
	updates := map[string]interface{}{"claimed_at": now, "ended_as": endedAs}
	if spunOffTo != nil {
		updates["spun_off_to_wallet_app_id"] = *spunOffTo
	}
	result := tx.Model(&db.CashWalletClaim{}).
		Where("id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL AND transfer_count = ?",
			ref.ClaimID, ref.IdentityType, ref.IdentityValue, ref.TransferCount).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrSliceClaimed
	}
	return nil
}

// ClaimCashSlicesTx claims every slice in refs, all or nothing: the first one
// already taken fails the call, and the caller's rollback undoes the rest.
func ClaimCashSlicesTx(tx *gorm.DB, refs []SliceRef, spunOffTo *uint, endedAs string, now time.Time) error {
	for i, ref := range refs {
		if err := ClaimCashSliceTx(tx, ref, spunOffTo, endedAs, now); err != nil {
			return fmt.Errorf("source %d: %w", i, err)
		}
	}
	return nil
}

// ReassignCashSliceIdentityTx moves an unclaimed slice to a new identity in place
// inside tx, guarded like ClaimCashSliceTx, and advances its version.
func ReassignCashSliceIdentityTx(tx *gorm.DB, ref SliceRef, newIdentityType, newIdentityValue, newIAPubkey string) error {
	result := tx.Model(&db.CashWalletClaim{}).
		Where("id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL AND transfer_count = ?",
			ref.ClaimID, ref.IdentityType, ref.IdentityValue, ref.TransferCount).
		Updates(map[string]interface{}{
			"identity_type":  newIdentityType,
			"identity_value": newIdentityValue,
			"ia_pubkey":      newIAPubkey,
			"transfer_count": ref.TransferCount + 1,
		})
	if result.Error != nil {
		if db.IsDuplicateKeyError(result.Error) {
			return ErrIdentityHoldsSlice
		}
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrSliceClaimed
	}
	return nil
}
