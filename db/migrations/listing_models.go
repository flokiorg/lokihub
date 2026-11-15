package migrations

import "github.com/flokiorg/lokihub/db"

// listingModels are the offer-listing tables (WP-25), kept in their own file so
// they rebase onto other schema changes without touching the main list. The
// schema is created fresh (no conversion), so there is no migration number.
func listingModels() []any {
	return []any{
		&db.OfferListing{},
		&db.ListingNodeState{},
	}
}
