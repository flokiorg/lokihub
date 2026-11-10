package api

import (
	"context"
	"fmt"
	"github.com/flokiorg/lokihub/clock"

	"github.com/flokiorg/lokihub/cash/cashorder"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/listing"
)

func (api *api) orderDeps() cashorder.Deps {
	return cashorder.Deps{DB: api.db, Keys: api.keys, Apps: api.appsSvc, LN: api.lnClient(), Relays: api.cfg.GetAdvertisedRelayUrls(), InfoCache: api.orderInfo}
}

func requireCashHub(hub *db.App) error {
	if hub.Kind != db.AppKindCashHub {
		return fmt.Errorf("%w: app is not a cash_hub", constants.ErrInvalidParams)
	}
	return nil
}

// CreateOrderConnection creates an Order connection for a Cash Hub and returns
// it with its signed cashorder1 string (NIP-CO §The Order Connection).
func (api *api) CreateOrderConnection(hub *db.App, label string) (*cashorder.Connection, error) {
	if err := requireCashHub(hub); err != nil {
		return nil, err
	}
	if api.lnClient() == nil {
		return nil, cashorder.ErrSignerUnavailable
	}
	return cashorder.CreateConnection(context.Background(), api.orderDeps(), hub.ID, label)
}

// ListOrderConnections lists a Cash Hub's Order connections with their strings.
func (api *api) ListOrderConnections(hub *db.App) ([]cashorder.Connection, error) {
	if err := requireCashHub(hub); err != nil {
		return nil, err
	}
	return cashorder.ListConnections(context.Background(), api.orderDeps(), hub.ID)
}

// RetireOrderConnection retires one of a Cash Hub's Order connections for good.
func (api *api) RetireOrderConnection(hub *db.App, connAppID uint) error {
	if err := requireCashHub(hub); err != nil {
		return err
	}
	if err := cashorder.RetireConnection(api.orderDeps(), hub.ID, connAppID, clock.Now()); err != nil {
		return err
	}
	// The retirement is committed, so get_info answers "cash_order": {} from
	// now on; then its listing is closed and deleted in order (WP-25 item 8).
	api.closeListings(func(l *listing.Service) error { return l.RetireOrder(context.Background(), connAppID) })
	return nil
}

// GetOrderTerms is a Cash Hub's selling terms and open-order limits; nil when
// it sells nothing yet.
func (api *api) GetOrderTerms(hub *db.App) (*cashorder.Settings, error) {
	if err := requireCashHub(hub); err != nil {
		return nil, err
	}
	s, ok, err := cashorder.GetSettings(api.db, hub.ID)
	if err != nil || !ok {
		return nil, err
	}
	return &s, nil
}

// PutOrderTerms sets a Cash Hub's selling terms; placed orders keep theirs.
func (api *api) PutOrderTerms(hub *db.App, s cashorder.Settings) error {
	if err := requireCashHub(hub); err != nil {
		return err
	}
	return cashorder.PutSettings(cashorder.Deps{DB: api.db, InfoCache: api.orderInfo}, hub.ID, s, clock.Now())
}

// ListOrders lists a Cash Hub's placed orders newest first and the payments it
// kept with no order, for the operator console (WP-19 item 8). It exposes only
// operator facts — no cash token and no buyer key.
func (api *api) ListOrders(hub *db.App) (*cashorder.OrdersView, error) {
	if err := requireCashHub(hub); err != nil {
		return nil, err
	}
	return cashorder.ListOrders(api.db, hub.ID, 500)
}
