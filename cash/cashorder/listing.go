package cashorder

import (
	"context"
	"fmt"

	"github.com/ohstr/nmilat/nipCO"
	nmlisting "github.com/ohstr/nmilat/nipcash/listing"

	"github.com/flokiorg/lokihub/connstr"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/listing"
)

// ListingSource is the listing service's view of Order connections (WP-25,
// listing.OrderSource): the signed connection, its Cash Hub, whether it is
// retired, and the kinds and terms it sells. Deps is read at each call, so the
// node may start after the source is built.
type ListingSource struct{ Deps func() Deps }

// Order resolves the Order connection connAppID; nil when the app is none.
func (s ListingSource) Order(ctx context.Context, connAppID uint) (*listing.OrderOffer, error) {
	deps := s.Deps()
	row, err := ConnectionOf(deps.DB, connAppID)
	if err != nil || row == nil {
		return nil, err
	}
	var app db.App
	if err := deps.DB.First(&app, connAppID).Error; err != nil {
		return nil, err
	}
	if app.WalletPubkey == nil {
		return nil, fmt.Errorf("order connection %d has no wallet key", connAppID)
	}
	secret, err := deps.Keys.GetCashPairingKey(connAppID)
	if err != nil {
		return nil, err
	}
	str, err := connstr.Order(ctx, deps.LN, *app.WalletPubkey, deps.Relays, secret, row.Label)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSignerUnavailable, err)
	}
	conn, err := nipCO.DecodeOrderConnection(str)
	if err != nil {
		return nil, err
	}
	offer := &listing.OrderOffer{Conn: &conn, HubAppID: row.HubAppID, Retired: row.RetiredAt != nil}
	settings, ok, err := GetSettings(deps.DB, row.HubAppID)
	if err != nil {
		return nil, err
	}
	if !ok || offer.Retired {
		return offer, nil
	}
	for _, kind := range []struct {
		payment, summary string
		terms            *nipCO.KindTerms
	}{
		{nipCO.PaymentLightning, nmlisting.SummaryLightning, settings.Terms.Lightning},
		{nipCO.PaymentOnchain, nmlisting.SummaryOnchain, settings.Terms.Onchain},
	} {
		if kind.terms == nil {
			continue
		}
		offer.Summary = append(offer.Summary, kind.summary)
		offer.Hints = append(offer.Hints, nmlisting.Hint{Payment: kind.payment, MinMillis: kind.terms.MinMillis,
			MaxMillis: kind.terms.MaxMillis, FeeBase: kind.terms.FeeBase, FeePpm: kind.terms.FeePpm})
	}
	return offer, nil
}
