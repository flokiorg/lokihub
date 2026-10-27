package api

import (
	"context"
	"fmt"
	"time"

	"github.com/flokiorg/lokihub/cashorder"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
)

func (api *api) orderDeps() cashorder.Deps {
	return cashorder.Deps{DB: api.db, Keys: api.keys, Apps: api.appsSvc, LN: api.lnClient(), Relays: api.cfg.GetRelayUrls()}
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
	return cashorder.RetireConnection(api.orderDeps(), hub.ID, connAppID, time.Now())
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
	return cashorder.PutSettings(api.db, hub.ID, s, time.Now())
}
