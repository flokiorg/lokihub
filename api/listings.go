package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nip01"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/connstr"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/events"
	"github.com/flokiorg/lokihub/listing"
	"github.com/flokiorg/lokihub/logger"
)

// ListingsAPI is the admin API of offer listings (WP-25), mirrored by Wails.
type ListingsAPI interface {
	ListListings(ctx context.Context) ([]listing.View, error)
	// CreateListing takes the raw JSON body so a body carrying any connection
	// string is refused (H-CASH-411).
	CreateListing(ctx context.Context, body []byte) (*listing.View, error)
	UpdateListing(ctx context.Context, id uint, body []byte) (*listing.View, error)
	// ListingAction runs pause, resume, unlist, revoke-connection or
	// check-operator.
	ListingAction(ctx context.Context, id uint, action string) (*listing.View, error)
	PreviewListing(ctx context.Context, id uint) (*nip01.Event, error)
	GetListingRelays() *ListingRelaysResponse
	SetListingRelays(ctx context.Context, relays []string) (*ListingRelaysResponse, error)
	RegenerateCashHubConnection(ctx context.Context, hubAppID uint) (*RegenerateCashHubConnectionResponse, error)
}

// ListingRelaysResponse is the listing relays setting (OQ-44): what is
// configured (empty: the default) and what is in effect.
type ListingRelaysResponse struct {
	Configured []string `json:"configured"`
	Effective  []string `json:"effective"`
}

// RegenerateCashHubConnectionResponse is the new cashhub1 string, shown once.
type RegenerateCashHubConnectionResponse struct {
	CashHubToken string `json:"cashHubToken"`
}

// ErrNodeNotStarted is a listing call before the node runs (503).
var ErrNodeNotStarted = errors.New("the node is not started")

func (api *api) listings() (*listing.Service, error) {
	if api.svc == nil {
		return nil, ErrNodeNotStarted
	}
	l := api.svc.GetListingService()
	if l == nil {
		return nil, ErrNodeNotStarted
	}
	return l, nil
}

func (api *api) ListListings(ctx context.Context) ([]listing.View, error) {
	l, err := api.listings()
	if err != nil {
		return nil, err
	}
	return l.List(ctx)
}

func (api *api) CreateListing(ctx context.Context, body []byte) (*listing.View, error) {
	if listing.CarriesConnectionString(body) {
		return nil, fmt.Errorf("%w: a listing is built from the Hub's own records; no connection string is taken", listing.ErrRefused)
	}
	l, err := api.listings()
	if err != nil {
		return nil, err
	}
	var req listing.CreateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("%w: %v", listing.ErrRefused, err)
	}
	return l.Create(ctx, req)
}

func (api *api) UpdateListing(ctx context.Context, id uint, body []byte) (*listing.View, error) {
	l, err := api.listings()
	if err != nil {
		return nil, err
	}
	if listing.CarriesConnectionString(body) {
		return nil, fmt.Errorf("%w: a listing is built from the Hub's own records; no connection string is taken", listing.ErrRefused)
	}
	var f listing.Fields
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("%w: %v", listing.ErrRefused, err)
	}
	return l.Update(ctx, id, f)
}

func (api *api) ListingAction(ctx context.Context, id uint, action string) (*listing.View, error) {
	l, err := api.listings()
	if err != nil {
		return nil, err
	}
	switch action {
	case "pause":
		return l.Pause(ctx, id)
	case "resume":
		return l.Resume(ctx, id)
	case "unlist":
		return l.Unlist(ctx, id)
	case "revoke-connection":
		return l.RevokeConnection(ctx, id)
	case "check-operator":
		return l.CheckOperator(ctx, id)
	}
	return nil, fmt.Errorf("%w: unknown action %q", listing.ErrRefused, action)
}

func (api *api) PreviewListing(ctx context.Context, id uint) (*nip01.Event, error) {
	l, err := api.listings()
	if err != nil {
		return nil, err
	}
	return l.Preview(ctx, id)
}

func (api *api) GetListingRelays() *ListingRelaysResponse {
	l, err := api.listings()
	if err != nil {
		return &ListingRelaysResponse{Configured: []string{}, Effective: []string{}}
	}
	return &ListingRelaysResponse{Configured: nonNil(l.ConfiguredListingRelays()), Effective: nonNil(l.ListingRelays())}
}

func (api *api) SetListingRelays(ctx context.Context, relays []string) (*ListingRelaysResponse, error) {
	l, err := api.listings()
	if err != nil {
		return nil, err
	}
	if err := l.SetListingRelays(ctx, relays); err != nil {
		return nil, err
	}
	return api.GetListingRelays(), nil
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// RegenerateCashHubConnection revokes a Cash Hub's connection at once and issues
// a new one (WP-25 item 7; NIP-CASH §Hub Rules: a cashhub1 whose string was
// published is revoked; owner LS-2): a new client key replaces the old in the
// same transaction, so the old string resolves to no app from then on. Bills,
// Order connections and records are untouched, and so is the hub group of every
// later bill (its input stays the key the Hub minted under first). The new string
// is returned once.
func (api *api) RegenerateCashHubConnection(ctx context.Context, hubAppID uint) (*RegenerateCashHubConnectionResponse, error) {
	var hub db.App
	if err := api.db.First(&hub, hubAppID).Error; err != nil {
		return nil, fmt.Errorf("%w: no such Cash Hub", listing.ErrNotFound)
	}
	if hub.Kind != db.AppKindCashHub || hub.WalletPubkey == nil {
		return nil, fmt.Errorf("%w: not a Cash Hub", listing.ErrRefused)
	}
	secret := nostr.GeneratePrivateKey()
	pub, err := nostr.GetPublicKey(secret)
	if err != nil {
		return nil, err
	}
	// Signed before anything changes: a node that cannot sign leaves the old
	// connection in place rather than the Hub with none.
	token, err := connstr.CashHub(ctx, api.lnClient(), *hub.WalletPubkey, api.cfg.GetRelayUrls(), secret, hubLabel(hub.Name))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", listing.ErrCannotSign, err)
	}
	err = api.db.Transaction(func(tx *gorm.DB) error {
		var cur db.App
		if err := tx.First(&cur, hub.ID).Error; err != nil {
			return fmt.Errorf("%w: no such Cash Hub", listing.ErrNotFound)
		}
		updates := map[string]any{"app_pubkey": pub}
		if cur.HubGroupPubkey == nil || *cur.HubGroupPubkey == "" {
			updates["hub_group_pubkey"] = cur.AppPubkey
		}
		res := tx.Model(&db.App{}).Where("id = ? AND app_pubkey = ?", cur.ID, cur.AppPubkey).Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			// A concurrent regenerate changed the key first: this token's key
			// was never stored (R2-L4).
			return fmt.Errorf("%w: the connection was regenerated concurrently; retry", listing.ErrRefused)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	logger.Logger.Error().Uint("app_id", hub.ID).
		Msg("The Cash Hub connection was published and has been revoked; a new connection was issued")
	api.eventPublisher.Publish(&events.Event{Event: "nwc_app_updated", Properties: map[string]interface{}{"id": hub.ID}})
	return &RegenerateCashHubConnectionResponse{CashHubToken: token}, nil
}
