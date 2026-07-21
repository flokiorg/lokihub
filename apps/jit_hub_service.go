package apps

import (
	"errors"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"gorm.io/gorm"
)

func (svc *appsService) CreateJITHub(name string, pubkey string, maxAmountLoki uint64, budgetRenewal string,
	expiresAt *time.Time, scopes []string, metadata map[string]interface{},
	config db.JITHubConfig) (*db.App, string, error) {

	if config.PerWalletMaxMloki <= 0 || config.MaxExpSecs <= 0 {
		return nil, "", fmt.Errorf("%w: per_wallet_max_mloki and max_exp_secs must be positive", constants.ErrInvalidParams)
	}

	app, secret, err := svc.CreateApp(name, pubkey, maxAmountLoki, budgetRenewal, expiresAt, scopes,
		db.AppKindJITHub, nil, "", metadata)
	if err != nil {
		return nil, "", err
	}

	config.AppID = app.ID
	if err := svc.db.Create(&config).Error; err != nil {
		_ = svc.DeleteApp(app)
		return nil, "", fmt.Errorf("failed to save JIT Hub config: %w", err)
	}

	return app, secret, nil
}

func (svc *appsService) GetJITHubConfig(appID uint) (*db.JITHubConfig, error) {
	var cfg db.JITHubConfig
	if err := svc.db.Where("app_id = ?", appID).First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("JIT Hub config not found for app %d: %w", appID, err)
	}
	return &cfg, nil
}

func (svc *appsService) UpdateJITHubConfig(appID uint, perWalletMaxMloki *int, maxExpSecs *int) error {
	updates := map[string]interface{}{}
	if perWalletMaxMloki != nil {
		if *perWalletMaxMloki <= 0 {
			return fmt.Errorf("%w: per_wallet_max_mloki must be positive", constants.ErrInvalidParams)
		}
		updates["per_wallet_max_mloki"] = *perWalletMaxMloki
	}
	if maxExpSecs != nil {
		if *maxExpSecs <= 0 {
			return fmt.Errorf("%w: max_exp_secs must be positive", constants.ErrInvalidParams)
		}
		updates["max_exp_secs"] = *maxExpSecs
	}
	if len(updates) == 0 {
		return nil
	}

	result := svc.db.Model(&db.JITHubConfig{}).Where("app_id = ?", appID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("JIT Hub config not found for app %d", appID)
	}
	return nil
}

// maxRecipientsPerWallet bounds how many recipients a single shared
// jit_wallet can serve — mirrors the old maxAllocationBatch cap on the
// now-deleted voucher table.
const maxRecipientsPerWallet = 100

// CreateJITWalletClaims batch-inserts one JITWalletClaim row per recipient of
// a freshly-created wallet. Called once by jitwallet.Commit right after the
// wallet app itself is created — all validation (identity shape, IA trust,
// amount caps, expiry) already happened in jitwallet.Resolve, so this is a
// pure insert.
func (svc *appsService) CreateJITWalletClaims(walletAppID uint, entries []db.JITWalletClaim) error {
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
	return svc.db.CreateInBatches(&entries, 50).Error
}

// ListClaimsForWallet returns every recipient slice of a single jit_wallet,
// claimed or not — the roster list_recipients exposes.
func (svc *appsService) ListClaimsForWallet(walletAppID uint) ([]db.JITWalletClaim, error) {
	var claims []db.JITWalletClaim
	err := svc.db.Where("wallet_app_id = ?", walletAppID).Order("created_at asc").Find(&claims).Error
	return claims, err
}

// ListJITHubWalletChildren returns every real jit_wallet child of hubID,
// queried directly from apps. See the interface doc comment.
func (svc *appsService) ListJITHubWalletChildren(hubID uint) ([]db.App, error) {
	var children []db.App
	err := svc.db.
		Where("parent_app_id = ? AND parent_kind = ? AND kind = ?", hubID, db.ParentKindJIT, db.AppKindJITWallet).
		Order("created_at asc").
		Find(&children).Error
	return children, err
}

// ListJITWalletClaims returns every JITWalletClaim belonging to any
// jit_wallet child of hubID, joined with that wallet's ExpiresAt, newest
// first. Unfiltered and unpaginated — the caller (api.ListJITWalletClaims)
// applies status filtering, counts, and pagination in memory, mirroring how
// the old merged allocations+children list worked.
func (svc *appsService) ListJITWalletClaims(hubID uint) ([]JITWalletClaimRow, error) {
	var rows []JITWalletClaimRow
	err := svc.db.Model(&db.JITWalletClaim{}).
		Joins("JOIN apps ON apps.id = jit_wallet_claims.wallet_app_id").
		Where("apps.parent_app_id = ? AND apps.parent_kind = ? AND apps.kind = ?",
			hubID, db.ParentKindJIT, db.AppKindJITWallet).
		Select("jit_wallet_claims.*, apps.expires_at AS wallet_expires_at").
		Order("jit_wallet_claims.created_at desc").
		Scan(&rows).Error
	return rows, err
}

// GetJITWalletClaim is a read-only lookup of one recipient's still-unclaimed
// slice. Returns nil, nil if no unclaimed row matches.
func (svc *appsService) GetJITWalletClaim(walletAppID uint, identityType, identityValue string) (*db.JITWalletClaim, error) {
	var claim db.JITWalletClaim
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

// ClaimJITWalletSlice atomically marks one recipient's slice claimed, guarded
// by "WHERE claimed_at IS NULL" so a replayed or racing claim can never
// double-pay. Returns the slice's AmountMloki on success.
func (svc *appsService) ClaimJITWalletSlice(walletAppID uint, identityType, identityValue string) (int64, error) {
	var claim db.JITWalletClaim
	if err := svc.db.Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NULL",
		walletAppID, identityType, identityValue).First(&claim).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, fmt.Errorf("%w: no unclaimed slice for this identity", constants.ErrInvalidParams)
		}
		return 0, err
	}
	now := time.Now()
	result := svc.db.Model(&db.JITWalletClaim{}).
		Where("id = ? AND claimed_at IS NULL", claim.ID).
		Update("claimed_at", now)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected == 0 {
		// Lost a race against a concurrent claim between the lookup above and this update.
		return 0, fmt.Errorf("%w: no unclaimed slice for this identity", constants.ErrInvalidParams)
	}
	return claim.AmountMloki, nil
}

// UnclaimJITWalletSlice reverts ClaimJITWalletSlice, guarded so it can only
// undo a slice that is currently claimed for this exact identity — never
// clobbers a different, legitimate claim. Used to roll back when the
// invoice-amount check or the payment itself subsequently fails.
func (svc *appsService) UnclaimJITWalletSlice(walletAppID uint, identityType, identityValue string) error {
	return svc.db.Model(&db.JITWalletClaim{}).
		Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ? AND claimed_at IS NOT NULL",
			walletAppID, identityType, identityValue).
		Update("claimed_at", nil).Error
}

// DeleteJITWalletClaim removes an unclaimed slice. The caller is responsible
// for sweeping its AmountMloki back to the hub before calling this — the
// returned row gives the caller the amount to sweep.
//
// The delete itself is conditioned on "AND claimed_at IS NULL", the same
// guard ClaimJITWalletSlice's own update uses, so a claim that gets claimed
// concurrently — after this function's own read above but before its delete
// runs — is never removed out from under the payout that just claimed it.
// Without that guard on the delete statement, that race can double-count
// funds: the concurrent claim pays the recipient, and this function still
// reports success to a caller who then sweeps the same amount back to the
// hub, since the row it read a moment ago still had claimed_at == nil.
func (svc *appsService) DeleteJITWalletClaim(walletAppID uint, claimID uint) (*db.JITWalletClaim, error) {
	var claim db.JITWalletClaim
	if err := svc.db.Where("id = ? AND wallet_app_id = ?", claimID, walletAppID).First(&claim).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: claim not found", constants.ErrInvalidParams)
		}
		return nil, err
	}
	if claim.ClaimedAt != nil {
		return nil, fmt.Errorf("%w: slice has already been claimed", constants.ErrInvalidParams)
	}
	result := svc.db.Where("id = ? AND claimed_at IS NULL", claim.ID).Delete(&db.JITWalletClaim{})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		// Lost a race against a concurrent claim between the read above and this delete.
		return nil, fmt.Errorf("%w: slice has already been claimed", constants.ErrInvalidParams)
	}
	return &claim, nil
}
