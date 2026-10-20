package nip47

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
	"github.com/flokiorg/lokihub/nip47/models"
)

// circleMemberGatedMethods move money out of a circle wallet or open a way for
// it to come in. A member removed from the circle can't call them; reading the
// balance and history still works, and an admin can still reclaim the funds.
var circleMemberGatedMethods = map[string]struct{}{
	models.PAY_INVOICE_METHOD:       {},
	models.PAY_KEYSEND_METHOD:       {},
	models.MULTI_PAY_INVOICE_METHOD: {},
	models.MULTI_PAY_KEYSEND_METHOD: {},
	models.MAKE_INVOICE_METHOD:      {},
	models.MAKE_HOLD_INVOICE_METHOD: {},
}

// circleMemberStillAuthorized decides whether a circle wallet's member may
// still call method. Membership is checked again on each call rather than
// stored, so removing the member freezes the wallet and adding them back
// unfreezes it. ok is false with the error code and message to answer with.
func (svc *nip47Service) circleMemberStillAuthorized(ctx context.Context, app *db.App, method string) (ok bool, code string, message string) {
	if app.Kind != db.AppKindCircleWallet {
		return true, "", ""
	}
	if _, gated := circleMemberGatedMethods[method]; !gated {
		return true, "", ""
	}

	// Written in the same transaction as the wallet, so a missing row means
	// there is no member to check: refused rather than trusted.
	var membership db.CircleWalletMembership
	if err := svc.db.Where("wallet_app_id = ?", app.ID).First(&membership).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			logger.Logger.Error().Uint("app_id", app.ID).Msg("Circle wallet has no membership row")
			return false, constants.ERROR_RESTRICTED, "this wallet has no circle member on record"
		}
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to load a circle wallet's membership")
		return false, constants.ERROR_INTERNAL, "failed to load circle membership"
	}
	hubConfig, err := svc.appsService.GetCircleHubConfig(membership.CircleHubAppID)
	if err != nil {
		logger.Logger.Error().Err(err).Uint("app_id", app.ID).Msg("Failed to load a circle wallet's hub")
		return false, constants.ERROR_INTERNAL, "failed to load the circle"
	}
	if svc.socialCache == nil {
		return false, constants.ERROR_NOT_READY, "circle membership can't be checked yet, please retry shortly"
	}

	authorized, err := svc.socialCache.IsAuthorized(ctx, membership.RequesterPubkey, &hubConfig.CircleIdentity, svc.db)
	if err != nil {
		// Can't decide is not "still a member": nothing moves until it can.
		logger.Logger.Warn().Err(err).Uint("app_id", app.ID).Msg("Circle membership check failed")
		return false, constants.ERROR_NOT_READY, "circle membership can't be checked yet, please retry shortly"
	}
	if !authorized {
		return false, constants.ERROR_RESTRICTED, "this wallet's member is no longer in the circle"
	}
	return true, "", ""
}
