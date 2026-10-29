package controllers

import (
	"context"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/getinfocache"
	"github.com/flokiorg/lokihub/nip47/models"
)

// InvalidateListedGetInfo drops a listed connection's cached get_info, so a
// change of terms, accepting or retirement is never served stale.
func InvalidateListedGetInfo(appID uint) {
	getinfocache.Listed.Invalidate(appID)
}

// HandleListingConnGetInfo answers get_info on a listed circle's own connection
// (a circle_hub_conn; WP-25 item 6) as its hub answers it: the same circle_wallet
// block, network, chain and identity, with methods ["get_info"] and the joins and
// lookups it serves on the private transport. It shows nothing of the hub's
// admin metadata: the connection is public.
func (controller *nip47Controller) HandleListingConnGetInfo(ctx context.Context, nip47Request *models.Request, requestEventId uint, conn *db.App, publishResponse publishFunc) {
	// Answered from a result at most getinfocache.TTL old: every tapper shares
	// the connection, so no per-client-key limit applies to it either.
	if result, ok := getinfocache.Listed.Get(conn.ID); ok {
		publishResponse(&models.Response{ResultType: nip47Request.Method, Result: result}, nostr.Tags{})
		return
	}
	stamp := getinfocache.Listed.Begin()
	var hub db.App
	if conn.ParentAppID == nil || controller.db.First(&hub, *conn.ParentAppID).Error != nil || hub.Kind != db.AppKindCircleHub {
		publishResponse(&models.Response{ResultType: nip47Request.Method,
			Error: &models.Error{Code: constants.ERROR_NOT_FOUND, Message: "this connection's circle is gone"}}, nostr.Tags{})
		return
	}
	controller.HandleGetInfoEvent(ctx, nip47Request, requestEventId, &hub, func(resp *models.Response, tags nostr.Tags) {
		if result, ok := resp.Result.(*getInfoResponse); ok && resp.Error == nil {
			result.Methods = []string{models.GET_INFO_METHOD}
			result.PrivateMethods = []string{constants.NIP47MethodCreateCircleWallet, constants.NIP47MethodCashLookup}
			result.Notifications = []string{}
			result.Metadata = nil
			result.LightningAddress = nil
			getinfocache.Listed.Put(conn.ID, stamp, result)
		}
		publishResponse(resp, tags)
	})
}
