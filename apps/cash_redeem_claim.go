package apps

import (
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/cashrequest"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// ErrRedeemInflight: the cash_redeem payouts in flight are at their bound, per
// Cash Hub or node-wide (NIP-CASH §Redeeming a Slice step 4: RATE_LIMITED,
// before the claim).
var ErrRedeemInflight = errors.New("too many cash_redeem payments are in flight")

// redeemLockKey serializes redeem claims node-wide on postgres, so the in-flight
// count and the claim it guards are one decision.
const redeemLockKey = 0x63617368_72646d // "cashrdm"

// RedeemClaim is one cash_redeem's claim: the slice exactly as the call read and
// authorized it, the payment that will redeem it and the claim proof it
// consumes ("" for a cash-mode slice).
type RedeemClaim struct {
	Slice          SliceRef
	HubAppID       uint
	PaymentHash    string
	ProofID        string
	ProofCreatedAt int64
	InflightPerHub int
	InflightNode   int
}

// inFlightRedeems counts claimed slices whose payout has started and recorded no
// preimage: the redeems in flight, counted from the claim itself so a payout
// whose ledger row is not written yet counts too. A payout the ledger shows
// settled is not in flight, even when its preimage never reached the slice.
func inFlightRedeems(tx *gorm.DB) *gorm.DB {
	return tx.Model(&db.CashWalletClaim{}).
		Where("cash_wallet_claims.claimed_at IS NOT NULL AND cash_wallet_claims.payment_hash <> '' AND cash_wallet_claims.preimage = ''").
		Where("NOT EXISTS (SELECT 1 FROM transactions WHERE transactions.app_id = cash_wallet_claims.wallet_app_id"+
			" AND transactions.type = ? AND transactions.state = ? AND transactions.payment_hash = cash_wallet_claims.payment_hash)",
			constants.TRANSACTION_TYPE_OUTGOING, constants.TRANSACTION_STATE_SETTLED)
}

// ClaimSliceForRedeem claims a slice for a cash_redeem in one transaction: the
// in-flight bound (ErrRedeemInflight), the first-wins claim on the exact identity
// and version authorized (ErrSliceClaimed), the payment hash, and the claim proof
// consumed (cashrequest.ErrProofConsumed). Nothing is written on any error.
func (svc *appsService) ClaimSliceForRedeem(c RedeemClaim, now time.Time) error {
	return svc.db.Transaction(func(tx *gorm.DB) error {
		if tx.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock($1)", int64(redeemLockKey)).Error; err != nil {
				return err
			}
		}
		if c.InflightNode > 0 {
			var n int64
			if err := inFlightRedeems(tx).Count(&n).Error; err != nil {
				return err
			}
			if n >= int64(c.InflightNode) {
				return ErrRedeemInflight
			}
		}
		if c.InflightPerHub > 0 {
			var n int64
			if err := inFlightRedeems(tx).
				Joins("JOIN apps ON apps.id = cash_wallet_claims.wallet_app_id").
				Where("apps.parent_app_id = ?", c.HubAppID).Count(&n).Error; err != nil {
				return err
			}
			if n >= int64(c.InflightPerHub) {
				return ErrRedeemInflight
			}
		}
		result := tx.Model(&db.CashWalletClaim{}).
			Where("id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL AND transfer_count = ?",
				c.Slice.ClaimID, c.Slice.IdentityType, c.Slice.IdentityValue, c.Slice.TransferCount).
			Updates(map[string]interface{}{
				"claimed_at":      now,
				"payment_hash":    c.PaymentHash,
				"redeem_proof_id": c.ProofID,
				"ended_as":        db.CashSliceStatusRedeemed,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSliceClaimed
		}
		if c.ProofID != "" {
			store := cashrequest.Store{DB: tx, Now: func() time.Time { return now }}
			if err := store.ConsumeProofTx(tx, c.ProofID, c.ProofCreatedAt, constants.NIP47MethodCashRedeem); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReleaseRedeemClaim reopens a slice whose redeem payment did not happen, or
// definitely failed: the claim and its consumed proof go in one transaction.
// cooldownUntil, when set, refuses another redeem of the slice until then. A
// payout the ledger shows pending or settled under paymentHash is never
// reopened. Reports whether a slice was released.
func (svc *appsService) ReleaseRedeemClaim(walletAppID uint, paymentHash string, cooldownUntil *time.Time) (bool, error) {
	if paymentHash == "" {
		return false, nil
	}
	var released bool
	err := svc.db.Transaction(func(tx *gorm.DB) error {
		var err error
		released, err = releaseRedeemClaimTx(tx, walletAppID, paymentHash, cooldownUntil, false)
		return err
	})
	return released, err
}

// releaseRedeemClaimTx is ReleaseRedeemClaim inside tx. requireFailed also asks
// the ledger to show the payment FAILED.
func releaseRedeemClaimTx(tx *gorm.DB, walletAppID uint, paymentHash string, cooldownUntil *time.Time, requireFailed bool) (bool, error) {
	ledger := func() *gorm.DB {
		return tx.Model(&db.Transaction{}).Where("app_id = ? AND type = ? AND payment_hash = ?",
			walletAppID, constants.TRANSACTION_TYPE_OUTGOING, paymentHash)
	}
	var live int64
	if err := ledger().Where("state IN ?", []string{constants.TRANSACTION_STATE_PENDING, constants.TRANSACTION_STATE_SETTLED}).Count(&live).Error; err != nil {
		return false, err
	}
	if live > 0 {
		return false, nil
	}
	if requireFailed {
		var failed int64
		if err := ledger().Where("state = ?", constants.TRANSACTION_STATE_FAILED).Count(&failed).Error; err != nil {
			return false, err
		}
		if failed == 0 {
			return false, nil
		}
	}
	var claim db.CashWalletClaim
	err := tx.Where("wallet_app_id = ? AND payment_hash = ? AND preimage = ? AND claimed_at IS NOT NULL",
		walletAppID, paymentHash, "").First(&claim).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	updates := map[string]interface{}{"claimed_at": nil, "payment_hash": "", "redeem_proof_id": "", "ended_as": ""}
	if cooldownUntil != nil {
		updates["redeem_cooldown_until"] = *cooldownUntil
	}
	result := tx.Model(&db.CashWalletClaim{}).
		Where("id = ? AND payment_hash = ? AND preimage = ? AND claimed_at IS NOT NULL", claim.ID, paymentHash, "").
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, nil
	}
	if claim.RedeemProofID != "" {
		if err := tx.Where("event_id = ?", claim.RedeemProofID).Delete(&db.CashConsumedProof{}).Error; err != nil {
			return false, err
		}
	}
	return true, nil
}

// redeemsAnswering holds the payment hashes whose cash_redeem is still deciding
// its own answer. While a hash is here the redeem settles its own outcome, so
// the payment-failed consumer leaves that slice alone: otherwise a failure the
// redeem answers PAYMENT_FAILED could be taken for a late one.
var redeemsAnswering sync.Map

// BeginRedeemAnswer marks paymentHash as owned by its cash_redeem.
func BeginRedeemAnswer(paymentHash string) { redeemsAnswering.Store(paymentHash, struct{}{}) }

// EndRedeemAnswer hands paymentHash's outcome back to the consumers.
func EndRedeemAnswer(paymentHash string) { redeemsAnswering.Delete(paymentHash) }

func redeemAnswering(paymentHash string) bool {
	_, ok := redeemsAnswering.Load(paymentHash)
	return ok
}

// lateFailureCooldown is when a slice whose payout failed after its redeem
// answered may be redeemed again; nil when the cool-down is off.
func (svc *appsService) lateFailureCooldown(now time.Time) *time.Time {
	if svc.cfg == nil {
		return nil
	}
	secs := svc.cfg.GetEnv().CashRedeemLateFailureCooldownSecs
	if secs <= 0 {
		return nil
	}
	until := now.Add(time.Duration(secs) * time.Second)
	return &until
}

// LateFailureCooldown is lateFailureCooldown for callers outside the package.
func (svc *appsService) LateFailureCooldown(now time.Time) *time.Time {
	return svc.lateFailureCooldown(now)
}
