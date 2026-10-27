package service

import (
	"context"

	"github.com/flokiorg/lokihub/connstr"
	"github.com/flokiorg/lokihub/listing"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/controllers"
)

// GetListingService is the offer-listing service (WP-25), nil until the node is
// started.
func (svc *service) GetListingService() *listing.Service {
	return svc.listingSvc
}

// startListings creates the listing service and starts its scheduler (WP-25 item
// 4), stopped with the app.
func (svc *service) startListings(ctx context.Context) {
	ln := func() lnclient.LNClient { return svc.lnClient }
	svc.listingSvc = listing.New(listing.Deps{
		DB:       svc.db,
		Settings: svc.cfg,
		Keys:     svc.keys,
		Signer:   listing.LNSigner{LN: ln},
		CircleString: func(ctx context.Context, pubkey string, relays []string, secret, label string) (string, error) {
			return connstr.CircleHub(ctx, ln(), pubkey, relays, secret, label)
		},
		Relays: listing.NostrRelays{},
		// Orders stays nil until Order connections exist (WP-21).
		InvalidateGetInfo: controllers.InvalidateListedGetInfo,
	})
	svc.shutdownGroup.Go(func() error {
		svc.listingSvc.Run(ctx)
		return nil
	})
}
