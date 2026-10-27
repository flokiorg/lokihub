package service

import (
	"context"
	"time"

	"github.com/flokiorg/lokihub/cashorder"
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

// SetListingClockOffset moves the listing scheduler's clock by secs (the
// regtest clock hook, scope "listing"; I-70, I-71).
func (svc *service) SetListingClockOffset(secs int64) {
	svc.listingClockOffset.Store(secs)
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
		Orders: cashorder.ListingSource{Deps: func() cashorder.Deps {
			return cashorder.Deps{DB: svc.db, Keys: svc.keys, LN: svc.lnClient, Relays: svc.cfg.GetRelayUrls()}
		}},
		InvalidateGetInfo: controllers.InvalidateListedGetInfo,
		// The listing clock: moved only by a testhooks build's clock hook
		// (scope "listing"); 0 otherwise.
		Now: func() time.Time {
			return time.Now().Add(time.Duration(svc.listingClockOffset.Load()) * time.Second)
		},
	})
	svc.shutdownGroup.Go(func() error {
		svc.listingSvc.Run(ctx)
		return nil
	})
}
