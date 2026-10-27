package cashorder

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/ohstr/nmilat/nipCO"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

// Settings are a Cash Hub's selling terms and its open-order limits: the
// ceiling on the open orders' amount per payment kind and the most open
// on-chain orders (0 = no count bound).
type Settings struct {
	Terms                    nipCO.Terms `json:"terms"`
	OpenCeilingLnMillis      uint64      `json:"openCeilingLightningMillis"`
	OpenCeilingOnchainMillis uint64      `json:"openCeilingOnchainMillis"`
	OpenOnchainMax           int         `json:"openOnchainMax"`
}

// Local bounds on the terms (refused, never clamped).
const (
	minLightningWindow = 60      // the invoice expiry
	maxLightningWindow = 86400   //
	minOnchainWindow   = 600     //
	maxOnchainWindow   = 604800  //
	maxFeePpm          = 1000000 // a ppm above a million is not a fraction
	// splitFloorDivisor: a kind's max_millis may not be below 1/100 of the
	// per-wallet ceiling, so one payment mints at most 100 bills per ceiling's
	// worth it brings in.
	splitFloorDivisor = 100
)

// ValidateSettings checks s against the Cash Hub's per-wallet funding ceiling
// (NIP-CO §Terms: max_millis never above it) and lokihub's bounds.
func ValidateSettings(s Settings, perWalletMaxMillis uint64) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", constants.ErrInvalidParams, fmt.Sprintf(format, a...))
	}
	if perWalletMaxMillis == 0 {
		return bad("the Cash Hub has no per-wallet funding ceiling")
	}
	if err := s.Terms.Validate(perWalletMaxMillis); err != nil {
		return bad("%v", err)
	}
	for _, kind := range []string{nipCO.PaymentLightning, nipCO.PaymentOnchain} {
		k := s.Terms.Kind(kind)
		if k == nil {
			continue
		}
		lo, hi, ceiling := uint64(minLightningWindow), uint64(maxLightningWindow), s.OpenCeilingLnMillis
		if kind == nipCO.PaymentOnchain {
			lo, hi, ceiling = minOnchainWindow, maxOnchainWindow, s.OpenCeilingOnchainMillis
			if k.MinMillis%1000 != 0 || k.MaxMillis%1000 != 0 {
				return bad("%s: amounts are whole base units", kind)
			}
		}
		switch {
		case k.Window < lo || k.Window > hi:
			return bad("%s: window must be %d to %d seconds", kind, lo, hi)
		case k.MinMillis == 0:
			return bad("%s: min_millis must be positive", kind)
		case k.FeePpm > maxFeePpm:
			return bad("%s: fee_ppm above %d", kind, maxFeePpm)
		case k.FeeBase > math.MaxInt64 || k.MaxMillis > math.MaxInt64:
			return bad("%s: an amount is too large", kind)
		case ceiling == 0 || ceiling > math.MaxInt64:
			return bad("%s: an open-order ceiling is required", kind)
		case k.MaxMillis < perWalletMaxMillis/splitFloorDivisor:
			return bad("%s: max_millis below 1/%d of the per-wallet ceiling", kind, splitFloorDivisor)
		}
	}
	if s.Terms.Bill.RedeemFeePpm > maxFeePpm {
		return bad("bill: redeem_fee_ppm above %d", maxFeePpm)
	}
	if s.Terms.Bill.RedeemFeeBase > math.MaxInt64 || s.Terms.Bill.MinTransferMillis > math.MaxInt64 {
		return bad("bill: an amount is too large")
	}
	if s.OpenOnchainMax < 0 {
		return bad("openOnchainMax is negative")
	}
	return nil
}

// GetSettings is a Cash Hub's settings; ok is false when it sells nothing yet.
func GetSettings(gormDB *gorm.DB, hubID uint) (s Settings, ok bool, err error) {
	var row db.CashOrderTerms
	err = gormDB.Where("hub_app_id = ?", hubID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Settings{}, false, nil
	}
	if err != nil {
		return Settings{}, false, err
	}
	if err := json.Unmarshal([]byte(row.TermsJSON), &s.Terms); err != nil {
		return Settings{}, false, err
	}
	s.OpenCeilingLnMillis = uint64(max(row.OpenCeilingLnMillis, 0))           //nolint:gosec // non-negative
	s.OpenCeilingOnchainMillis = uint64(max(row.OpenCeilingOnchainMillis, 0)) //nolint:gosec // non-negative
	s.OpenOnchainMax = row.OpenOnchainMax
	return s, true, nil
}

// PutSettings validates and stores a Cash Hub's settings. New orders take them;
// each placed order keeps the terms it was placed under.
func PutSettings(gormDB *gorm.DB, hubID uint, s Settings, now time.Time) error {
	return gormDB.Transaction(func(tx *gorm.DB) error {
		if err := db.LockApp(tx, hubID); err != nil {
			return err
		}
		var hub db.App
		if err := tx.First(&hub, hubID).Error; err != nil {
			return err
		}
		if hub.Kind != db.AppKindCashHub {
			return fmt.Errorf("%w: app %d is not a Cash Hub", constants.ErrInvalidParams, hubID)
		}
		var cfg db.CashHubConfig
		if err := tx.Where("app_id = ?", hubID).First(&cfg).Error; err != nil {
			return err
		}
		if err := ValidateSettings(s, uint64(max(cfg.PerWalletMaxMloki, 0))); err != nil { //nolint:gosec // non-negative
			return err
		}
		termsJSON, err := json.Marshal(s.Terms)
		if err != nil {
			return err
		}
		row := db.CashOrderTerms{HubAppID: hubID, TermsJSON: string(termsJSON),
			OpenCeilingLnMillis: int64(s.OpenCeilingLnMillis), OpenCeilingOnchainMillis: int64(s.OpenCeilingOnchainMillis), //nolint:gosec // validated
			OpenOnchainMax: s.OpenOnchainMax, UpdatedAt: now.Unix()}
		return tx.Save(&row).Error
	})
}
