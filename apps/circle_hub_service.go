package apps

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// validateBudgetRenewal reports whether renewal is one of
// constants.GetBudgetRenewals() — shared by create and update validation so
// both reject the same set of values.
func validateBudgetRenewal(renewal string) error {
	if !slices.Contains(constants.GetBudgetRenewals(), renewal) {
		return fmt.Errorf("%w: min_budget_renewal must be one of %s, got %q",
			constants.ErrInvalidParams, strings.Join(constants.GetBudgetRenewals(), ","), renewal)
	}
	return nil
}

// CircleIdentityRef selects which CircleIdentity a new circle_hub should
// use: either an existing one (ExistingID set — reused as-is, Name/Policy/
// ProviderPubkey below are ignored), or a brand-new one created from the
// remaining fields.
type CircleIdentityRef struct {
	ExistingID     *uint
	Name           string
	Policy         string
	ProviderPubkey string
}

func (svc *appsService) CreateCircleHub(name string, pubkey string, maxAmountLoki uint64, budgetRenewal string,
	expiresAt *time.Time, scopes []string, metadata map[string]interface{},
	identityRef CircleIdentityRef, config db.CircleHubConfig) (*db.App, string, error) {

	if config.MaxExpSecs <= 0 || config.PerWalletMaxMloki <= 0 {
		return nil, "", fmt.Errorf("%w: max_exp_secs and per_wallet_max_mloki must be positive", constants.ErrInvalidParams)
	}
	if config.FeesPpm < 0 || config.FeesPpm > constants.MAX_FEES_PPM {
		return nil, "", fmt.Errorf("%w: fees_ppm must be between 0 and %d", constants.ErrInvalidParams, constants.MAX_FEES_PPM)
	}
	if config.MinBudgetRenewal == "" {
		config.MinBudgetRenewal = constants.BUDGET_RENEWAL_MONTHLY
	}
	if err := validateBudgetRenewal(config.MinBudgetRenewal); err != nil {
		return nil, "", err
	}

	var identityID uint
	if identityRef.ExistingID != nil {
		identity, err := svc.GetCircleIdentity(*identityRef.ExistingID)
		if err != nil {
			return nil, "", err
		}
		identityID = identity.ID
	} else {
		identity, err := svc.CreateCircleIdentity(identityRef.Name, identityRef.Policy, identityRef.ProviderPubkey)
		if err != nil {
			return nil, "", err
		}
		identityID = identity.ID
	}

	app, secret, err := svc.CreateApp(name, pubkey, maxAmountLoki, budgetRenewal, expiresAt, scopes,
		db.AppKindCircleHub, nil, "", metadata)
	if err != nil {
		return nil, "", err
	}

	config.AppID = app.ID
	config.CircleIdentityID = identityID
	if err := svc.db.Create(&config).Error; err != nil {
		_ = svc.DeleteApp(app)
		// The identity is intentionally left behind on this failure path — it's
		// designed to outlive any single provider, including one that failed to
		// finish being created; either an orphaned brand-new identity (harmless,
		// reusable later) or an existing shared identity that must not be touched.
		return nil, "", fmt.Errorf("failed to save Circle Hub config: %w", err)
	}

	return app, secret, nil
}

func (svc *appsService) GetCircleHubConfig(appID uint) (*db.CircleHubConfig, error) {
	var cfg db.CircleHubConfig
	if err := svc.db.Preload("CircleIdentity").Where("app_id = ?", appID).First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("circle hub config not found for app %d: %w", appID, err)
	}
	return &cfg, nil
}

func (svc *appsService) UpdateCircleHubConfig(appID uint, maxExpSecs *int, feesPpm *int,
	perWalletMaxMloki *int, minBudgetRenewal *string) error {
	updates := map[string]interface{}{}
	if maxExpSecs != nil {
		if *maxExpSecs <= 0 {
			return fmt.Errorf("%w: max_exp_secs must be positive", constants.ErrInvalidParams)
		}
		updates["max_exp_secs"] = *maxExpSecs
	}
	if feesPpm != nil {
		if *feesPpm < 0 || *feesPpm > constants.MAX_FEES_PPM {
			return fmt.Errorf("%w: fees_ppm must be between 0 and %d", constants.ErrInvalidParams, constants.MAX_FEES_PPM)
		}
		updates["fees_ppm"] = *feesPpm
	}
	if perWalletMaxMloki != nil {
		if *perWalletMaxMloki <= 0 {
			return fmt.Errorf("%w: per_wallet_max_mloki must be positive", constants.ErrInvalidParams)
		}
		updates["per_wallet_max_mloki"] = *perWalletMaxMloki
	}
	if minBudgetRenewal != nil {
		if err := validateBudgetRenewal(*minBudgetRenewal); err != nil {
			return err
		}
		updates["min_budget_renewal"] = *minBudgetRenewal
	}
	if len(updates) == 0 {
		return nil
	}

	result := svc.db.Model(&db.CircleHubConfig{}).Where("app_id = ?", appID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("circle hub config not found for app %d", appID)
	}
	return nil
}
