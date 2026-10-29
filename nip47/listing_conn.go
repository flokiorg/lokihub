package nip47

import (
	"context"
	"fmt"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/controllers"
	"github.com/flokiorg/lokihub/nip47/models"
)

// walletKeyAppID is the app whose wallet key decrypts app's requests and signs
// its replies. A listed circle's own connection shares its hub's wallet key (the
// same TLV 0, NIP-CASH §Hub Rules), so a request encrypted to TLV 0 decrypts and
// the reply is authored by TLV 0 (§Connection Signatures, online check).
func walletKeyAppID(app *db.App) uint {
	if app.Kind == db.AppKindCircleHubConn && app.ParentAppID != nil {
		return *app.ParentAppID
	}
	return app.ID
}

// serveListingConn serves a 23194 request on a listed circle's own connection:
// get_info as the hub, every other method NOT_IMPLEMENTED (joins and their
// lookups travel on the private transport, served by the hub).
func (svc *nip47Service) serveListingConn(ctx context.Context, lnClient lnclient.LNClient, req *models.Request, requestEventID uint, app *db.App, publish func(*models.Response, nostr.Tags)) {
	if req.Method != models.GET_INFO_METHOD {
		publish(&models.Response{ResultType: req.Method, Error: &models.Error{
			Code:    constants.ERROR_NOT_IMPLEMENTED,
			Message: fmt.Sprintf("%s is not served on a listing connection: joins travel on the private transport", req.Method),
		}}, nostr.Tags{})
		return
	}
	controllers.NewNip47Controller(lnClient, svc.db, svc.eventPublisher, svc.permissionsService, svc.transactionsService, svc.appsService, svc.keys, svc.socialCache, svc.cashRateLimiter, svc.cashClaimLimiter, svc.circleRateLimiter, svc.cfg, svc.identityAuthorityMgr).
		HandleListingConnGetInfo(ctx, req, requestEventID, app, publish)
}
