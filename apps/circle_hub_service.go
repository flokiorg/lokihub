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
// use: either an existing one (ExistingID set — reused as-is, the fields
// below are ignored), or a brand-new one created from the remaining fields.
type CircleIdentityRef struct {
	ExistingID     *uint
	Name           string
	Policy         string
	ProviderPubkey string
	FollowSetD     string
}

func (svc *appsService) CreateCircleHub(name string, pubkey string, maxAmountLoki uint64, budgetRenewal string,
	expiresAt *time.Time, scopes []string, metadata map[string]interface{},
	identityRef CircleIdentityRef, config db.CircleHubConfig) (*db.App, string, error) {

	if err := validateHubLabel(name); err != nil {
		return nil, "", err
	}
	// A receive ceiling left out takes the send ceiling, and a default lifetime
	// left out under a ceiling takes the ceiling: what a join got before either
	// setting existed.
	if config.PerWalletMaxReceiveMloki == 0 {
		config.PerWalletMaxReceiveMloki = int64(config.PerWalletMaxMloki)
	}
	if config.DefaultExpSecs == 0 {
		config.DefaultExpSecs = config.MaxExpSecs
	}
	if config.MinBudgetRenewal == "" {
		config.MinBudgetRenewal = constants.BUDGET_RENEWAL_MONTHLY
	}
	if err := validateCircleHubConfig(&config); err != nil {
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
		identity, err := svc.CreateCircleIdentity(identityRef.Name, identityRef.Policy, identityRef.ProviderPubkey, identityRef.FollowSetD)
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

// CircleHubConfigPatch is a circle hub settings update: a nil field leaves that
// setting unchanged.
type CircleHubConfigPatch struct {
	MaxExpSecs               *int
	DefaultExpSecs           *int
	FeesPpm                  *int
	PerWalletMaxMloki        *int
	PerWalletMaxReceiveMloki *int64
	MinBudgetRenewal         *string
}

// IsEmpty reports whether the patch changes nothing.
func (p CircleHubConfigPatch) IsEmpty() bool {
	return p == CircleHubConfigPatch{}
}

// apply writes the patch's set fields onto cfg.
func (p CircleHubConfigPatch) apply(cfg *db.CircleHubConfig) {
	if p.MaxExpSecs != nil {
		cfg.MaxExpSecs = *p.MaxExpSecs
	}
	if p.DefaultExpSecs != nil {
		cfg.DefaultExpSecs = *p.DefaultExpSecs
	}
	if p.FeesPpm != nil {
		cfg.FeesPpm = *p.FeesPpm
	}
	if p.PerWalletMaxMloki != nil {
		cfg.PerWalletMaxMloki = *p.PerWalletMaxMloki
	}
	if p.PerWalletMaxReceiveMloki != nil {
		cfg.PerWalletMaxReceiveMloki = *p.PerWalletMaxReceiveMloki
	}
	if p.MinBudgetRenewal != nil {
		cfg.MinBudgetRenewal = *p.MinBudgetRenewal
	}
}

// patchedCircleHubConfig is appID's stored config with the patch applied and
// checked as a whole, so a setting is judged against the others it depends on
// (a default lifetime against the ceiling, whichever of the two changes).
func (svc *appsService) patchedCircleHubConfig(appID uint, patch CircleHubConfigPatch) (*db.CircleHubConfig, error) {
	var cfg db.CircleHubConfig
	if err := svc.db.Where("app_id = ?", appID).First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("circle hub config not found for app %d: %w", appID, err)
	}
	patch.apply(&cfg)
	if err := validateCircleHubConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (svc *appsService) ValidateCircleHubConfigUpdate(appID uint, patch CircleHubConfigPatch) error {
	_, err := svc.patchedCircleHubConfig(appID, patch)
	return err
}

func (svc *appsService) UpdateCircleHubConfig(appID uint, patch CircleHubConfigPatch) error {
	if patch.IsEmpty() {
		return nil
	}
	cfg, err := svc.patchedCircleHubConfig(appID, patch)
	if err != nil {
		return err
	}
	result := svc.db.Model(&db.CircleHubConfig{}).Where("app_id = ?", appID).Updates(map[string]interface{}{
		"max_exp_secs":                 cfg.MaxExpSecs,
		"default_exp_secs":             cfg.DefaultExpSecs,
		"fees_ppm":                     cfg.FeesPpm,
		"per_wallet_max_mloki":         cfg.PerWalletMaxMloki,
		"per_wallet_max_receive_mloki": cfg.PerWalletMaxReceiveMloki,
		"min_budget_renewal":           cfg.MinBudgetRenewal,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("circle hub config not found for app %d", appID)
	}
	return nil
}

// validateCircleHubConfig checks a whole circle hub config (NIP-CW §Data Model):
// the lifetime ceiling and default each 0..MAX_EXPIRY_SECS with 0 meaning none
// or never, the default not above a non-zero ceiling nor 0 under one; both
// per-wallet ceilings; the fee rate; the renewal floor.
func validateCircleHubConfig(cfg *db.CircleHubConfig) error {
	if cfg.MaxExpSecs < 0 || cfg.MaxExpSecs > constants.MAX_EXPIRY_SECS {
		return fmt.Errorf("%w: max_exp_secs must be between 0 (no ceiling) and %d", constants.ErrInvalidParams, constants.MAX_EXPIRY_SECS)
	}
	if cfg.DefaultExpSecs < 0 || cfg.DefaultExpSecs > constants.MAX_EXPIRY_SECS {
		return fmt.Errorf("%w: default_exp_secs must be between 0 (never) and %d", constants.ErrInvalidParams, constants.MAX_EXPIRY_SECS)
	}
	if cfg.MaxExpSecs > 0 && cfg.DefaultExpSecs > cfg.MaxExpSecs {
		return fmt.Errorf("%w: default_exp_secs must not exceed max_exp_secs", constants.ErrInvalidParams)
	}
	if cfg.MaxExpSecs > 0 && cfg.DefaultExpSecs == 0 {
		return fmt.Errorf("%w: default_exp_secs must not be 0 (never) under a non-zero max_exp_secs", constants.ErrInvalidParams)
	}
	if err := validatePerWalletMaxMloki(cfg.PerWalletMaxMloki); err != nil {
		return err
	}
	if cfg.PerWalletMaxReceiveMloki <= 0 {
		return fmt.Errorf("%w: per_wallet_max_receive_mloki must be positive", constants.ErrInvalidParams)
	}
	if cfg.FeesPpm < 0 || cfg.FeesPpm > constants.MAX_FEES_PPM {
		return fmt.Errorf("%w: fees_ppm must be between 0 and %d", constants.ErrInvalidParams, constants.MAX_FEES_PPM)
	}
	return validateBudgetRenewal(cfg.MinBudgetRenewal)
}

// validatePerWalletMaxMloki refuses a per-wallet ceiling below the smallest cap a
// member could usefully join with (constants.MIN_CIRCLE_WALLET_MAX_MLOKI): no
// wallet under it could ever pay outside this node.
func validatePerWalletMaxMloki(perWalletMaxMloki int) error {
	if perWalletMaxMloki < constants.MIN_CIRCLE_WALLET_MAX_MLOKI {
		return fmt.Errorf("%w: per_wallet_max_mloki must be at least %d (the 10-loki fee reserve plus 1 loki)",
			constants.ErrInvalidParams, constants.MIN_CIRCLE_WALLET_MAX_MLOKI)
	}
	return nil
}
