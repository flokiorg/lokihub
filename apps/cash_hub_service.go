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
	// The transfer_count re-check closes a SEPARATE, newer race: a concurrent
	// SplitCashSliceAmount call can shrink this row's AmountMloki (a partial
	// split) WITHOUT changing identity_type/identity_value or claimed_at at
	// all — the identity re-check above does nothing to catch that. Pinning
	// transfer_count (which SplitCashSliceAmount always increments, on both
	// its full and partial branches, exactly like ReassignCashSliceIdentity
	// does) means any concurrent mutation to this row between the read above
	// and this update — identity reassignment OR amount split — invalidates
	// this claim attempt, so the AmountMloki this function returns is always
	// the row's true, current value, never a stale pre-split snapshot that
	// would let a redeem pay out more than the slice's real remaining balance.
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
		Update("claimed_at", nil).Error
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

// SplitCashSliceAmount atomically moves splitMloki out of one unclaimed
// slice — either fully (claiming it terminal, exactly like a real redemption)
// or partially (decrementing its AmountMloki, leaving it alive under the same
// identity with the remainder). See the AppsService interface doc comment for
// the full contract; this is the one place both cash_transfer outcomes (full
// split and partial split) share their atomicity guard, so they can't drift
// apart the way two separate methods eventually would.
//
// Race-safety: the read below captures TransferCount and AmountMloki, then
// the committing UPDATE pins BOTH of them in its WHERE clause as an
// optimistic lock — mirroring ReassignCashSliceIdentity's TransferCount pin,
// extended to also cover AmountMloki since this is the one method that
// mutates it. Any concurrent mutation of this row (a redeem, a reassignment,
// or another split) between the read and this update changes at least one of
// those two columns, so the loser's update always matches zero rows and
// reports ErrNotFound — never a stale, silently-wrong amount.
func (svc *appsService) SplitCashSliceAmount(walletAppID uint, identityType, identityValue string, splitMloki int64) (CashSliceSplitResult, error) {
	if splitMloki <= 0 {
		return CashSliceSplitResult{}, fmt.Errorf("%w: amount_mloki must be positive", constants.ErrInvalidParams)
	}
	var claim db.CashWalletClaim
	if err := svc.db.Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL",
		walletAppID, identityType, identityValue).First(&claim).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return CashSliceSplitResult{}, fmt.Errorf("%w: no slice registered for this identity", constants.ErrNotFound)
		}
		return CashSliceSplitResult{}, err
	}
	if splitMloki > claim.AmountMloki {
		return CashSliceSplitResult{}, fmt.Errorf("%w: amount_mloki %d exceeds this slice's own balance of %d",
			constants.ErrInvalidParams, splitMloki, claim.AmountMloki)
	}
	remainder := claim.AmountMloki - splitMloki
	// MinTransferMloki floors BOTH sides of a split: the piece being carved
	// off, and whatever's left behind (unless the remainder is exactly zero —
	// a full split leaves nothing to be dust). Without the remainder check, a
	// split could strand an unmovable sliver on the source slice that could
	// never itself be split further, only redeemed or transferred whole.
	if claim.MinTransferMloki > 0 {
		if splitMloki < claim.MinTransferMloki {
			return CashSliceSplitResult{}, fmt.Errorf("%w: amount_mloki %d is below this slice's min_transfer_mloki %d",
				constants.ErrInvalidParams, splitMloki, claim.MinTransferMloki)
		}
		if remainder > 0 && remainder < claim.MinTransferMloki {
			return CashSliceSplitResult{}, fmt.Errorf(
				"%w: this split would leave a remainder of %d, below min_transfer_mloki %d — transfer it all instead, or transfer less",
				constants.ErrInvalidParams, remainder, claim.MinTransferMloki)
		}
	}

	result := CashSliceSplitResult{
		SplitAmountMloki:     splitMloki,
		FullyDrained:         remainder == 0,
		RemainingAmountMloki: remainder,
		MinTransferMloki:     claim.MinTransferMloki,
		RedeemFeePpm:         claim.RedeemFeePpm,
	}

	var dbResult *gorm.DB
	if remainder == 0 {
		// Full split: claim the slice terminal, same shape as a redemption.
		// AmountMloki is untouched (the whole value is leaving, nothing to
		// shrink) — still pinned in the WHERE clause below as the optimistic
		// lock, so a concurrent PARTIAL split that shrank this row between the
		// read above and this update correctly invalidates this attempt
		// instead of claiming a stale, too-large amount as if it were current.
		dbResult = svc.db.Model(&db.CashWalletClaim{}).
			Where("id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL AND transfer_count = ? AND amount_mloki = ?",
				claim.ID, identityType, identityValue, claim.TransferCount, claim.AmountMloki).
			Update("claimed_at", time.Now())
	} else {
		// Partial split: decrement AmountMloki by splitMloki using the value
		// read above (not live SQL arithmetic) — race-safety comes from
		// pinning both TransferCount and AmountMloki in the WHERE clause as an
		// optimistic lock, exactly like every other atomic method here, not
		// from evaluating the subtraction against the database's live value.
		dbResult = svc.db.Model(&db.CashWalletClaim{}).
			Where("id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL AND transfer_count = ? AND amount_mloki = ?",
				claim.ID, identityType, identityValue, claim.TransferCount, claim.AmountMloki).
			Updates(map[string]interface{}{
				"amount_mloki":   remainder,
				"transfer_count": claim.TransferCount + 1,
			})
	}
	if dbResult.Error != nil {
		return CashSliceSplitResult{}, dbResult.Error
	}
	if dbResult.RowsAffected == 0 {
		// Lost a race against a concurrent claim, transfer, or split of this
		// same row between the read above and this update.
		return CashSliceSplitResult{}, fmt.Errorf("%w: this slice has already been redeemed or transferred", constants.ErrNotFound)
	}
	return result, nil
}

// UndoCashSliceSplit reverts a PARTIAL SplitCashSliceAmount call: adds
// splitMloki back to the slice's AmountMloki and decrements TransferCount,
// guarded by "claimed_at IS NULL" so it can only apply to a slice that's
// still alive. Used when the new dedicated wallet's creation/funding
// subsequently fails, mirroring UnclaimCashSlice's rollback posture but for
// the amount dimension. Do not call this to roll back a FULL split — that
// path never touches AmountMloki/TransferCount at all until the row is
// claimed, so a plain UnclaimCashSlice call already restores it correctly.
//
// Returns an error wrapping constants.ErrNotFound (matching every sibling
// atomic method's "lost the race" convention) if the row was legitimately
// claimed/transferred/split again in the window between the failed split's
// own decrement and this rollback — RowsAffected == 0 is NOT silently
// treated as success (it used to be, silently stranding the carved-off
// amount with no matching CashWalletClaim row anywhere, though the real
// ledger balance stays untouched and is eventually recovered by the expiry
// sweep regardless). Callers MUST log this case at
// Error level with enough detail to locate and manually sweep the balance
// immediately rather than waiting on that sweep.
func (svc *appsService) UndoCashSliceSplit(walletAppID uint, identityType, identityValue string, splitMloki int64) error {
	result := svc.db.Model(&db.CashWalletClaim{}).
		Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL",
			walletAppID, identityType, identityValue).
		Updates(map[string]interface{}{
			"amount_mloki":   gorm.Expr("amount_mloki + ?", splitMloki),
			"transfer_count": gorm.Expr("transfer_count - 1"),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: slice was claimed/mutated again before the split rollback could run", constants.ErrNotFound)
	}
	return nil
}

// SetCashSliceSplitTarget records which new wallet a just-claimed
// slice's value was spun off into — purely informational (see
// db.CashWalletClaim.SpunOffToWalletAppID doc comment). Called only after the
// new wallet has been successfully created and funded, so the guard here is
// defensive rather than load-bearing: nothing else can be racing this exact
// row once SplitCashSliceAmount's full-split branch has claimed it.
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
		Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NOT NULL AND payment_hash = ?",
			walletAppID, identityType, identityValue, "").
		Updates(map[string]interface{}{
			"payment_hash":      payment.PaymentHash,
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
