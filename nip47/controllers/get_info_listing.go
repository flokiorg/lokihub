package controllers

import (
	"context"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
)

// listedGetInfoTTL is how long a listed connection's get_info is answered from
// its cached result (NIP-CASH §Hub Rules MAY; plan OQ-47). Every tapper shares
// the connection, so no per-client-key limit applies to it either.
const listedGetInfoTTL = 5 * time.Second

type cachedGetInfo struct {
	at     time.Time
	result *getInfoResponse
}

var listedGetInfoCache sync.Map // app id -> cachedGetInfo

// InvalidateListedGetInfo drops a listed connection's cached get_info, so a
// change of terms, accepting or retirement is never served stale.
func InvalidateListedGetInfo(appID uint) {
	listedGetInfoCache.Delete(appID)
}

// HandleListingConnGetInfo answers get_info on a listed circle's own connection
// (a circle_hub_conn; WP-25 item 6) as its hub answers it: the same circle_wallet
// block, network, chain and identity, with methods ["get_info"] and the joins and
// lookups it serves on the private transport. It shows nothing of the hub's
// admin metadata: the connection is public.
func (controller *nip47Controller) HandleListingConnGetInfo(ctx context.Context, nip47Request *models.Request, requestEventId uint, conn *db.App, publishResponse publishFunc) {
	if c, ok := listedGetInfoCache.Load(conn.ID); ok {
		if entry := c.(cachedGetInfo); time.Since(entry.at) < listedGetInfoTTL {
			publishResponse(&models.Response{ResultType: nip47Request.Method, Result: entry.result}, nostr.Tags{})
			return
		}
	}
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
			listedGetInfoCache.Store(conn.ID, cachedGetInfo{at: time.Now(), result: result})
		}
		publishResponse(resp, tags)
	})
}
