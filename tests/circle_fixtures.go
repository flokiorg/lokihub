package tests

import (
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// AddCircleHubConfig gives a hub app made directly in the database the config
// every Circle Wallet Hub has: an allowlist identity and the money model's
// defaults (a 1 FLC holdings ceiling, the default routing-fee reserve), changed
// by mutate. Tests that build a hub row by hand need it for any circle payment.
func AddCircleHubConfig(svc *TestService, hubID uint, mutate ...func(*db.CircleHubConfig)) (*db.CircleHubConfig, error) {
	identity := db.CircleIdentity{Name: "fixture", Policy: db.CirclePolicyAllowlist}
	if err := svc.DB.Create(&identity).Error; err != nil {
		return nil, err
	}
	cfg := db.CircleHubConfig{
		AppID:                    hubID,
		CircleIdentityID:         identity.ID,
		PerWalletMaxMloki:        100_000_000,
		PerWalletMaxReceiveMloki: 100_000_000,
		MinBudgetRenewal:         constants.BUDGET_RENEWAL_NEVER,
		HoldingsCeilingMloki:     100_000_000_000,
		FeeReservePpm:            constants.CIRCLE_DEFAULT_FEE_RESERVE_PPM,
		FeeReserveMinMloki:       constants.CIRCLE_DEFAULT_FEE_RESERVE_MIN_MLOKI,
		OutboundPeriod:           constants.CIRCLE_DEFAULT_OUTBOUND_PERIOD,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	if err := svc.DB.Create(&cfg).Error; err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SetCircleMaxReceive sets a circle wallet's receive cap.
func SetCircleMaxReceive(svc *TestService, walletID uint, maxReceiveMloki int64) error {
	return svc.DB.Model(&db.App{}).Where("id = ?", walletID).Update("circle_max_receive_mloki", maxReceiveMloki).Error
}
