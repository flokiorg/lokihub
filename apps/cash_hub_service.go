package apps

import (
	"errors"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/archive"
	"github.com/flokiorg/lokihub/events"
	"gorm.io/gorm"
)

// validateTransferFloorCoversFee refuses a split floor that sits below the flat
// redeem fee.
//
// Such a pair is self-contradictory rather than merely lax: min_transfer_mloki exists
// so a split cannot produce a piece too small to be worth anything, and a piece at or
// below redeem_fee_base_mloki cannot be redeemed to Lightning at all — the fee
// consumes it and cash_redeem now refuses a zero payout outright. So a floor below the
// base claims to prevent exactly what it permits.
//
// A floor of 0 is NOT rejected, deliberately. 0 means "no floor" in both this field's
// own documentation and every enforcement site, and silently promoting it to the fee
// base would reinterpret a meaningful zero — the same mistake that made a negative
// value read as "unset" above, and the same shape as the bugs this round found in
// spent_retention_secs and identity_required. An operator who wants no floor may have
// one, and the consequence is dust that can only be consolidated, not redeemed. Making
// that the default is a policy change for an operator to make, not a validation rule.
func validateTransferFloorCoversFee(minTransferMloki, redeemFeeBaseMloki int64) error {
	if minTransferMloki > 0 && minTransferMloki < redeemFeeBaseMloki {
		return fmt.Errorf("%w: min_transfer_mloki (%d) must not be below redeem_fee_base_mloki (%d) — "+
			"a split floor beneath the flat fee permits pieces that cannot be redeemed at all",
			constants.ErrInvalidParams, minTransferMloki, redeemFeeBaseMloki)
	}
	return nil
}

// maxHubLabelBytes is the cashhub1/circlehub1 label TLV's limit (NIP-CASH §The Cash
// Hub Connection, NIP-CW §The Circle Wallet Hub Connection): one length byte.
const maxHubLabelBytes = 255

// validateHubLabel refuses a hub name its connection token could not carry. The
// token is only encoded after the hub is saved, so a longer name used to leave a
// hub whose one-time connection secret was never shown.
func validateHubLabel(name string) error {
	if len(name) > maxHubLabelBytes {
		return fmt.Errorf("%w: a hub name can be at most %d bytes, got %d", constants.ErrInvalidParams, maxHubLabelBytes, len(name))
	}
	return nil
}

func (svc *appsService) CreateCashHub(name string, pubkey string, maxAmountLoki uint64, budgetRenewal string,
	expiresAt *time.Time, scopes []string, metadata map[string]interface{},
	config db.CashHubConfig) (*db.App, string, error) {

	// MaxExpSecs may be 0 ("never" — no ceiling on how long an issued wallet
	// may remain unredeemed); PerWalletMaxMloki has no equivalent "unlimited"
	// mode and must stay strictly positive. The upper bound guards against
	// overflowing time.Duration's nanosecond range when this value is later
	// converted (cashwallet.Resolve) — see constants.MAX_EXPIRY_SECS.
	if config.PerWalletMaxMloki <= 0 || config.MaxExpSecs < 0 || config.MaxExpSecs > constants.MAX_EXPIRY_SECS {
		return nil, "", fmt.Errorf("%w: per_wallet_max_mloki must be positive and max_exp_secs must be between 0 and %d",
			constants.ErrInvalidParams, constants.MAX_EXPIRY_SECS)
	}
	if config.RedeemFeePpm < 0 || config.RedeemFeePpm > constants.MAX_FEES_PPM {
		return nil, "", fmt.Errorf("%w: redeem_fee_ppm must be between 0 and %d", constants.ErrInvalidParams, constants.MAX_FEES_PPM)
	}
	// 0 is meaningful here (tombstone disabled — go silent the moment a bill
	// is destroyed), so only a negative or overflowing value is rejected.
	if config.SpentRetentionSecs < 0 || config.SpentRetentionSecs > constants.MAX_EXPIRY_SECS {
		return nil, "", fmt.Errorf("%w: spent_retention_secs must be between 0 and %d", constants.ErrInvalidParams, constants.MAX_EXPIRY_SECS)
	}
	// These two were validated on UPDATE and not here, so this path accepted configs
	// the other refuses. A negative min_transfer_mloki is the dangerous one: every
	// enforcement site tests `> 0`, so a negative value reads as "no floor" and
	// silently disables the guard the operator thought they had set.
	//
	// 0 stays meaningful in both ("no floor", "no base") — see UpdateCashHubConfig.
	if config.MinTransferMloki < 0 {
		return nil, "", fmt.Errorf("%w: min_transfer_mloki must not be negative", constants.ErrInvalidParams)
	}
	if config.RedeemFeeBaseMloki < 0 {
		return nil, "", fmt.Errorf("%w: redeem_fee_base_mloki must not be negative", constants.ErrInvalidParams)
	}
	if err := validateTransferFloorCoversFee(config.MinTransferMloki, config.RedeemFeeBaseMloki); err != nil {
		return nil, "", err
	}
	if err := validateHubLabel(name); err != nil {
		return nil, "", err
	}

	app, secret, err := svc.CreateApp(name, pubkey, maxAmountLoki, budgetRenewal, expiresAt, scopes,
		db.AppKindCashHub, nil, "", metadata)
	if err != nil {
		return nil, "", err
	}

	config.AppID = app.ID
	if err := svc.db.Create(&config).Error; err != nil {
		_ = svc.DeleteApp(app)
		return nil, "", fmt.Errorf("failed to save Cash Hub config: %w", err)
	}

	return app, secret, nil
}

func (svc *appsService) GetCashHubConfig(appID uint) (*db.CashHubConfig, error) {
	var cfg db.CashHubConfig
	if err := svc.db.Where("app_id = ?", appID).First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("cash hub config not found for app %d: %w", appID, err)
	}
	return &cfg, nil
}

func (svc *appsService) UpdateCashHubConfig(appID uint, perWalletMaxMloki *int, maxExpSecs *int, minTransferMloki *int64, redeemFeePpm *int, redeemFeeBaseMloki *int64, spentRetentionSecs *int) error {
	updates := map[string]interface{}{}
	if perWalletMaxMloki != nil {
		if *perWalletMaxMloki <= 0 {
			return fmt.Errorf("%w: per_wallet_max_mloki must be positive", constants.ErrInvalidParams)
		}
		updates["per_wallet_max_mloki"] = *perWalletMaxMloki
	}
	if maxExpSecs != nil {
		// Unlike per_wallet_max_mloki above, 0 is a valid, meaningful value
		// here ("never" — no ceiling on how long an issued wallet may remain
		// unredeemed) — reject a negative one, or one large enough to
		// overflow time.Duration's nanosecond range when later converted
		// (cashwallet.Resolve) — see constants.MAX_EXPIRY_SECS.
		if *maxExpSecs < 0 || *maxExpSecs > constants.MAX_EXPIRY_SECS {
			return fmt.Errorf("%w: max_exp_secs must be between 0 and %d", constants.ErrInvalidParams, constants.MAX_EXPIRY_SECS)
		}
		updates["max_exp_secs"] = *maxExpSecs
	}
	if minTransferMloki != nil {
		// Unlike the two floors above, 0 is a valid, meaningful value here
		// ("no floor") — only reject a negative one.
		if *minTransferMloki < 0 {
			return fmt.Errorf("%w: min_transfer_mloki must not be negative", constants.ErrInvalidParams)
		}
		updates["min_transfer_mloki"] = *minTransferMloki
	}
	if redeemFeePpm != nil {
		if *redeemFeePpm < 0 || *redeemFeePpm > constants.MAX_FEES_PPM {
			return fmt.Errorf("%w: redeem_fee_ppm must be between 0 and %d", constants.ErrInvalidParams, constants.MAX_FEES_PPM)
		}
		updates["redeem_fee_ppm"] = *redeemFeePpm
	}
	if redeemFeeBaseMloki != nil {
		// Only a floor. There is deliberately no ceiling beyond what a slice can bear:
		// CalculateRedeemFeeMloki already caps the fee at the amount it is charged on,
		// and an operator who sets a base larger than the bills they mint is quoting a
		// zero payout, which is a pricing mistake rather than a safety one.
		if *redeemFeeBaseMloki < 0 {
			return fmt.Errorf("%w: redeem_fee_base_mloki must not be negative", constants.ErrInvalidParams)
		}
		updates["redeem_fee_base_mloki"] = *redeemFeeBaseMloki
	}
	if spentRetentionSecs != nil {
		// As on create: 0 means "no tombstone", not "unset".
		if *spentRetentionSecs < 0 || *spentRetentionSecs > constants.MAX_EXPIRY_SECS {
			return fmt.Errorf("%w: spent_retention_secs must be between 0 and %d", constants.ErrInvalidParams, constants.MAX_EXPIRY_SECS)
		}
		updates["spent_retention_secs"] = *spentRetentionSecs
	}
	if len(updates) == 0 {
		return nil
	}
	// Checked against the pair that will be IN FORCE afterwards, not against what this
	// call happens to carry: either field may be updated alone, so raising the fee base
	// on a hub with a low floor is just as incoherent as setting both at once, and
	// checking only the supplied values would miss it.
	if minTransferMloki != nil || redeemFeeBaseMloki != nil {
		current, err := svc.GetCashHubConfig(appID)
		if err != nil {
			return err
		}
		effectiveFloor := current.MinTransferMloki
		if minTransferMloki != nil {
			effectiveFloor = *minTransferMloki
		}
		effectiveBase := current.RedeemFeeBaseMloki
		if redeemFeeBaseMloki != nil {
			effectiveBase = *redeemFeeBaseMloki
		}
		if err := validateTransferFloorCoversFee(effectiveFloor, effectiveBase); err != nil {
			return err
		}
	}

	result := svc.db.Model(&db.CashHubConfig{}).Where("app_id = ?", appID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("cash hub config not found for app %d", appID)
	}

	// A retention change must reach bills this Hub has ALREADY destroyed, not
	// only future ones, so the materialised deadline on each archive row is
	// rewritten to match. Lowering therefore shortens windows already quoted to
	// callers as retained_until, and raising can make an expired bill answerable
	// again — the price of retention being live policy rather than a figure
	// frozen at destruction time.
	//
	// Batched, but synchronous: an operator changing this should not get a
	// success back while some of their bills still answer under the old policy.
	if spentRetentionSecs != nil {
		retention := time.Duration(*spentRetentionSecs) * time.Second
		if err := db.RecomputeSpentRetention(svc.db, appID, retention, false); err != nil {
			return fmt.Errorf("failed to apply new spent retention to archived bills: %w", err)
		}

		// Tells the service layer to re-sync its relay gate: raising retention
		// brings wallets back into range that the sweep has already dropped, and
		// nothing else would notice until a restart.
		svc.eventPublisher.Publish(&events.Event{
			Event: "nwc_cash_retention_changed",
			Properties: map[string]interface{}{
				"id": appID,
			},
		})
	}

	return nil
}

// maxRecipientsPerWallet bounds how many recipients a single shared
// cash_wallet can serve — mirrors the old maxAllocationBatch cap on the
// now-deleted voucher table.
const maxRecipientsPerWallet = 100

// CreateCashWalletClaims batch-inserts one CashWalletClaim row per recipient of
// a freshly-created wallet. Called once by cashwallet.Commit right after the
// wallet app itself is created — all validation (identity shape, IA trust,
// amount caps, expiry) already happened in cashwallet.Resolve, so this is a
// pure insert.
func (svc *appsService) CreateCashWalletClaims(walletAppID uint, entries []db.CashWalletClaim) error {
	return svc.CreateCashWalletClaimsTx(svc.db, walletAppID, entries)
}

// CreateCashWalletClaimsTx is CreateCashWalletClaims run inside a
// caller-provided transaction — see the AppsService interface doc comment.
// tx must be the caller's own open transaction handle when called from inside
// one (never svc.db independently — see CreateAppTx/prepareApp's own doc
// comment on why an independent read/write against the same underlying DB
// while a transaction is open can deadlock).
func (svc *appsService) CreateCashWalletClaimsTx(tx *gorm.DB, walletAppID uint, entries []db.CashWalletClaim) error {
	if len(entries) == 0 {
		return fmt.Errorf("%w: recipients list is empty", constants.ErrInvalidParams)
	}
	if len(entries) > maxRecipientsPerWallet {
		return fmt.Errorf("%w: at most %d recipients per wallet, got %d",
			constants.ErrInvalidParams, maxRecipientsPerWallet, len(entries))
	}
	for i := range entries {
		entries[i].WalletAppID = walletAppID
	}
	return tx.CreateInBatches(&entries, 50).Error
}

// ListClaimsForWallet returns every recipient slice of a single cash_wallet,
// claimed or not — the roster cash_status exposes.
func (svc *appsService) ListClaimsForWallet(walletAppID uint) ([]db.CashWalletClaim, error) {
	var claims []db.CashWalletClaim
	err := svc.db.Where("wallet_app_id = ?", walletAppID).Order("created_at asc").Find(&claims).Error
	return claims, err
}

// HasArchivedSliceForWallet implements AppsService — see the interface for why
// existence of an archive row is the signal, and why it is the only way to
// recover a wallet's lifetime recipient count.
//
// LIMIT 1 rather than COUNT: the caller asks "is the lifetime set bigger than the
// live one", never by how much. Measured at ~35µs on an indexed wallet_app_id and
// flat from 200k to 2M archive rows (db/cash_lifetime_count_bench_test.go).
func (svc *appsService) HasArchivedSliceForWallet(walletAppID uint) (bool, error) {
	var found int64
	err := svc.db.Raw(
		`SELECT 1 FROM cash_bill_slice_archives WHERE wallet_app_id = ? LIMIT 1`,
		walletAppID,
	).Scan(&found).Error
	if err != nil {
		return false, err
	}
	return found == 1, nil
}

// ListCashHubWalletChildren returns every real cash_wallet child of hubID,
// queried directly from apps. See the interface doc comment.
func (svc *appsService) ListCashHubWalletChildren(hubID uint) ([]db.App, error) {
	var children []db.App
	err := svc.db.
		Where("parent_app_id = ? AND parent_kind = ? AND kind = ?", hubID, db.ParentKindCash, db.AppKindCashWallet).
		Order("created_at asc").
		Find(&children).Error
	return children, err
}

// GetCashWalletClaim is a read-only lookup of one recipient's still-unclaimed
// slice. Returns nil, nil if no unclaimed row matches.
func (svc *appsService) GetCashWalletClaim(walletAppID uint, identityType, identityValue string) (*db.CashWalletClaim, error) {
	var claim db.CashWalletClaim
	err := svc.db.Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL",
		walletAppID, identityType, identityValue).First(&claim).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &claim, nil
}

// ClaimCashSlice atomically marks one recipient's slice claimed, guarded
// by "WHERE claimed_at IS NULL" so a replayed or racing claim can never
// double-pay. Returns the slice's AmountMloki on success.
func (svc *appsService) ClaimCashSlice(walletAppID uint, identityType, identityValue string) (int64, error) {
	var claim db.CashWalletClaim
	if err := svc.db.Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL",
		walletAppID, identityType, identityValue).First(&claim).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, fmt.Errorf("%w: no slice registered for this identity", constants.ErrNotFound)
		}
		return 0, err
	}
	now := time.Now()
	// The identity re-check here (not just id + claimed_at IS NULL) closes a
	// TOCTOU race against a concurrent cash_transfer reassignment of this same
	// row: ReassignCashSliceIdentity can reassign identity_type/identity_value
	// on an unclaimed row without ever setting claimed_at, so between the
	// lookup above and this update, this slice's registered identity may no
	// longer be the one that was just verified. Without re-checking it here,
	// this update would still match on id alone and pay out the PRE-transfer
	// caller — a phantom transfer.
	//
	// The transfer_count re-check pins the row version too, which
	// ReassignCashSliceIdentity increments, so any concurrent in-place transfer
	// between the read above and this update invalidates this claim attempt.
	// AmountMloki itself is never rewritten in place, so the value returned is
	// the slice's true amount.
	result := svc.db.Model(&db.CashWalletClaim{}).
		Where("id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL AND transfer_count = ?",
			claim.ID, identityType, identityValue, claim.TransferCount).
		Update("claimed_at", now)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		// Lost a race against a concurrent claim, or a concurrent transfer/
		// split that mutated this slice, between the lookup above and this
		// update.
		return 0, fmt.Errorf("%w: this slice has already been redeemed", constants.ErrNotFound)
	}
	return claim.AmountMloki, nil
}

// UnclaimCashSlice reverts ClaimCashSlice, guarded so it can only
// undo a slice that is currently claimed for this exact identity — never
// clobbers a different, legitimate claim. Used to roll back when the
// invoice-amount check or the payment itself subsequently fails.
func (svc *appsService) UnclaimCashSlice(walletAppID uint, identityType, identityValue string) error {
	return svc.db.Model(&db.CashWalletClaim{}).
		Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NOT NULL",
			walletAppID, identityType, identityValue).
		Updates(map[string]interface{}{"claimed_at": nil, "payment_hash": ""}).Error
}

func (svc *appsService) SetCashSliceRedeemPaymentHash(walletAppID uint, identityType, identityValue, paymentHash string) error {
	return svc.db.Model(&db.CashWalletClaim{}).
		Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NOT NULL AND payment_hash = ?",
			walletAppID, identityType, identityValue, "").
		Update("payment_hash", paymentHash).Error
}

// ReleaseCashSliceForFailedPayment is how a slice comes back after cash_redeem
// had to keep it claimed because its payment's outcome was unknown. The ledger
// must show that payment FAILED and nothing pending or settled under its hash,
// so a payout that went through is never reopened.
func (svc *appsService) ReleaseCashSliceForFailedPayment(walletAppID uint, paymentHash string) (bool, error) {
	if paymentHash == "" {
		return false, nil
	}
	var released int64
	err := svc.db.Transaction(func(tx *gorm.DB) error {
		var failed, live int64
		ledger := func() *gorm.DB {
			return tx.Model(&db.Transaction{}).Where("app_id = ? AND type = ? AND payment_hash = ?",
				walletAppID, constants.TRANSACTION_TYPE_OUTGOING, paymentHash)
		}
		if err := ledger().Where("state = ?", constants.TRANSACTION_STATE_FAILED).Count(&failed).Error; err != nil {
			return err
		}
		if err := ledger().Where("state IN ?", []string{constants.TRANSACTION_STATE_PENDING, constants.TRANSACTION_STATE_SETTLED}).Count(&live).Error; err != nil {
			return err
		}
		if failed == 0 || live > 0 {
			return nil
		}
		result := tx.Model(&db.CashWalletClaim{}).
			Where("wallet_app_id = ? AND payment_hash = ? AND preimage = ? AND claimed_at IS NOT NULL",
				walletAppID, paymentHash, "").
			Updates(map[string]interface{}{"claimed_at": nil, "payment_hash": ""})
		released = result.RowsAffected
		return result.Error
	})
	return released > 0, err
}

// ReassignCashSliceIdentity atomically reassigns one unclaimed slice's
// registered identity. The read below captures TransferCount once, then the
// update's own WHERE clause re-checks that exact value (an optimistic lock)
// so two concurrent transfers of the same slice — each having read the same
// pre-transfer count — can't both succeed: the loser's update matches zero
// rows because the winner already advanced TransferCount out from under it.
// Mirrors ClaimCashSlice's identical two-step shape and its "no such
// slice" reporting for a lost race.
func (svc *appsService) ReassignCashSliceIdentity(walletAppID uint, currentIdentityType, currentIdentityValue,
	newIdentityType, newIdentityValue, newIAPubkey string) (int64, error) {
	var claim db.CashWalletClaim
	if err := svc.db.Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL",
		walletAppID, currentIdentityType, currentIdentityValue).First(&claim).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, fmt.Errorf("%w: no slice registered for this identity", constants.ErrNotFound)
		}
		return 0, err
	}
	result := svc.db.Model(&db.CashWalletClaim{}).
		Where("id = ? AND claimed_at IS NULL AND transfer_count = ?", claim.ID, claim.TransferCount).
		Updates(map[string]interface{}{
			"identity_type":  newIdentityType,
			"identity_value": newIdentityValue,
			"ia_pubkey":      newIAPubkey,
			"transfer_count": claim.TransferCount + 1,
		})
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		// Lost a race against a concurrent claim or transfer of the same
		// slice between the lookup above and this update. This is a timing
		// outcome, not a malformed request — constants.ErrNotFound (not
		// ErrInvalidParams) lets the caller (cash_transfer_controller.go) map
		// it to NOT_FOUND, the same code cash_redeem's identical race-loss
		// case returns, rather than a misleading BAD_REQUEST.
		return 0, fmt.Errorf("%w: this slice has already been redeemed or transferred", constants.ErrNotFound)
	}
	return claim.AmountMloki, nil
}

// SetCashSliceSplitTarget records which new wallet a just-claimed
// slice's value was spun off into — purely informational (see
// db.CashWalletClaim.SpunOffToWalletAppID doc comment). Called only after the
// new wallet has been successfully created and funded, so the guard here is
// defensive rather than load-bearing: nothing else can be racing this exact
// row once the split has claimed it (ClaimCashSlice).
func (svc *appsService) SetCashSliceSplitTarget(walletAppID uint, identityType, identityValue string, newWalletAppID uint) error {
	return svc.db.Model(&db.CashWalletClaim{}).
		Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NOT NULL AND spun_off_to_wallet_app_id IS NULL",
			walletAppID, identityType, identityValue).
		Update("spun_off_to_wallet_app_id", newWalletAppID).Error
}

// SetCashSliceRedeemPayment records the payout facts for a slice that was just
// redeemed over Lightning — purely informational (see the payment-fact fields
// on db.CashWalletClaim), and the redeem-side mirror of
// SetCashSliceSplitTarget.
//
// Recorded here, at redeem time, because it cannot be recovered later: a
// Transaction carries no identity, so nothing links a payout back to its slice
// on a multi-recipient bill, and the transaction rows cascade away with the
// wallet app long before a spent bill is archived.
//
// Called only after SendPaymentSync has returned a settled transaction, so the
// guard is defensive rather than load-bearing — the slice is already terminally
// claimed by then, and a failed payment is rolled back by UnclaimCashSlice
// instead of reaching here.
func (svc *appsService) SetCashSliceRedeemPayment(walletAppID uint, identityType, identityValue string, payment CashSliceRedeemPayment) error {
	return svc.db.Model(&db.CashWalletClaim{}).
		Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NOT NULL AND payment_hash IN ?",
			walletAppID, identityType, identityValue, []string{"", payment.PaymentHash}).
		Updates(map[string]interface{}{
			"payment_hash":      payment.PaymentHash,
			"preimage":          payment.Preimage,
			"redeem_fee_mloki":  payment.RedeemFeeMloki,
			"routing_fee_mloki": payment.RoutingFeeMloki,
			"settled_at":        payment.SettledAt,
		}).Error
}

// RecordCashSliceRedeemSettlement records a redeem payout's facts on the slice
// that paid it, found by payment hash, for a payout that settled after
// cash_redeem had already answered (or that the reconcile found settled at
// startup). Only a claimed slice still missing its preimage is written, so a
// slice cash_redeem has already recorded is left exactly as it is.
func (svc *appsService) RecordCashSliceRedeemSettlement(walletAppID uint, payment CashSliceRedeemPayment) error {
	if payment.PaymentHash == "" || payment.Preimage == "" {
		return nil
	}
	return svc.db.Model(&db.CashWalletClaim{}).
		Where("wallet_app_id = ? AND payment_hash = ? AND claimed_at IS NOT NULL AND preimage = ?",
			walletAppID, payment.PaymentHash, "").
		Updates(map[string]interface{}{
			"preimage":          payment.Preimage,
			"redeem_fee_mloki":  payment.RedeemFeeMloki,
			"routing_fee_mloki": payment.RoutingFeeMloki,
			"settled_at":        payment.SettledAt,
		}).Error
}

// SetCashWalletSplitSource records, on the NEW wallet's own App row, which
// source cash_wallet it was split from — purely informational (see
// db.App.SplitFromWalletAppID doc comment), the reverse of
// SetCashSliceSplitTarget. Called only after the new wallet has been
// successfully created and funded.
func (svc *appsService) SetCashWalletSplitSource(newWalletAppID, sourceWalletAppID uint) error {
	return svc.db.Model(&db.App{}).
		Where("id = ? AND split_from_wallet_app_id IS NULL", newWalletAppID).
		Update("split_from_wallet_app_id", sourceWalletAppID).Error
}

// DeleteCashBill archives a cash bill's history and hard-deletes it, in one
// transaction, then announces the deletion.
//
// This is the only way to delete a cash_wallet: AppsService.DeleteApp refuses
// that kind outright, so a delete path added later cannot silently skip the
// archive and destroy a bill's history without anyone noticing.
//
// outcome is a db.CashBillOutcome* value describing how the bill ended. Callers
// that first reclaim a balance should use service.ReclaimAndDeleteSubWallet
// instead — it does that reclaim (which cannot sit inside a transaction) before
// delegating here.
func (svc *appsService) DeleteCashBill(app *db.App, outcome string) error {
	if err := svc.db.Transaction(func(tx *gorm.DB) error {
		return archive.ArchiveAndDeleteCashBillTx(tx, app, outcome, 0, time.Now())
	}); err != nil {
		return err
	}
	// After the commit: the consumer looks the app up over its own connection,
	// so announcing mid-transaction would race it.
	svc.eventPublisher.Publish(&events.Event{
		Event: "nwc_app_deleted",
		Properties: map[string]interface{}{
			"name": app.Name,
			"id":   app.ID,
		},
	})
	return nil
}

// DeleteCashClaim removes an unclaimed slice. The caller is responsible
// for sweeping its AmountMloki back to the hub before calling this — the
// returned row gives the caller the amount to sweep.
//
// The delete itself is conditioned on "AND claimed_at IS NULL", the same
// guard ClaimCashSlice's own update uses, so a claim that gets claimed
// concurrently — after this function's own read above but before its delete
// runs — is never removed out from under the payout that just claimed it.
// Without that guard on the delete statement, that race can double-count
// funds: the concurrent claim pays the recipient, and this function still
// reports success to a caller who then sweeps the same amount back to the
// hub, since the row it read a moment ago still had claimed_at == nil.
func (svc *appsService) DeleteCashClaim(walletAppID uint, claimID uint) (*db.CashWalletClaim, error) {
	var claim db.CashWalletClaim
	if err := svc.db.Where("id = ? AND wallet_app_id = ?", claimID, walletAppID).First(&claim).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: claim not found", constants.ErrInvalidParams)
		}
		return nil, err
	}
	if claim.ClaimedAt != nil {
		return nil, fmt.Errorf("%w: slice has already been claimed", constants.ErrInvalidParams)
	}

	var wallet db.App
	if err := svc.db.First(&wallet, walletAppID).Error; err != nil {
		return nil, fmt.Errorf("failed to read cash bill %d: %w", walletAppID, err)
	}

	// Archive the slice in the same transaction that destroys it. The bill
	// itself lives on, so the bill-death archive would never see this
	// recipient — without this they would vanish with no record that they were
	// ever owed anything, which is exactly the gap an operator later needs
	// explained. "reclaimed", not "expired": their window had not passed, they
	// were cut off.
	if err := svc.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND claimed_at IS NULL", claim.ID).Delete(&db.CashWalletClaim{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// Lost a race against a concurrent claim between the read above and this delete.
			return fmt.Errorf("%w: slice has already been claimed", constants.ErrInvalidParams)
		}
		return archive.ArchiveCashSliceTx(tx, &wallet, claim, db.CashSliceStatusReclaimed, time.Now())
	}); err != nil {
		return nil, err
	}
	return &claim, nil
}
