// Package cashorder sells a Cash Hub's cash through Order connections (NIP-CO):
// the connections themselves, the Hub's selling terms, and placing an order.
package cashorder

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/nbd-wtf/go-nostr"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/connstr"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/getinfocache"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/lnclient"
)

// ErrSignerUnavailable: the node could not sign the connection string, so no
// connection was created (answered 503).
var ErrSignerUnavailable = errors.New("the node could not sign the order connection")

// orderScopes are an Order connection's grants: get_info on the standard
// transport; cash_order and cash_lookup travel on the private transport, gated
// by the app kind.
var orderScopes = []string{constants.GET_INFO_SCOPE}

// Deps is what this package needs from the node.
type Deps struct {
	DB     *gorm.DB
	Keys   keys.Keys
	Apps   apps.AppsService
	LN     lnclient.LNClient
	Relays []string
}

// Connection is one Order connection of a Cash Hub, with its cashorder1 string
// (shown again on demand: its secret is public and re-derived).
type Connection struct {
	AppID            uint   `json:"id"`
	HubAppID         uint   `json:"hubId"`
	Label            string `json:"label"`
	WalletPubkey     string `json:"walletPubkey"`
	RetiredAt        *int64 `json:"retiredAt,omitempty"`
	CreatedAt        int64  `json:"createdAt"`
	ConnectionString string `json:"connectionString,omitempty"`
}

// maxLabelBytes is TLV 3's limit (one length byte).
const maxLabelBytes = 255

// CreateConnection creates an Order connection for the Cash Hub hubID and returns
// it with its signed cashorder1 string. A signer failure creates nothing.
func CreateConnection(ctx context.Context, deps Deps, hubID uint, label string) (*Connection, error) {
	if len(label) > maxLabelBytes || !utf8.ValidString(label) {
		return nil, fmt.Errorf("%w: the label is at most %d bytes of UTF-8", constants.ErrInvalidParams, maxLabelBytes)
	}
	var out *Connection
	var created *db.App
	err := deps.DB.Transaction(func(tx *gorm.DB) error {
		var hub db.App
		if err := tx.First(&hub, hubID).Error; err != nil {
			return err
		}
		if hub.Kind != db.AppKindCashHub {
			return fmt.Errorf("%w: app %d is not a Cash Hub", constants.ErrInvalidParams, hubID)
		}
		name := label
		if name == "" {
			name = "order"
		}
		app, _, err := deps.Apps.CreateAppTx(tx, apps.GenerateChildName(hub.Name, name), "", 0,
			constants.BUDGET_RENEWAL_NEVER, nil, orderScopes, db.AppKindCashOrder, &hub.ID, db.ParentKindCash, nil)
		if err != nil {
			return fmt.Errorf("create the order connection: %w", err)
		}
		secret, err := deps.Keys.GetCashPairingKey(app.ID)
		if err != nil {
			return err
		}
		pubkey, err := nostr.GetPublicKey(secret)
		if err != nil {
			return err
		}
		if err := tx.Model(&db.App{}).Where("id = ?", app.ID).Update("app_pubkey", pubkey).Error; err != nil {
			return err
		}
		app.AppPubkey = pubkey
		row := db.CashOrderConnection{AppID: app.ID, HubAppID: hub.ID, Label: label, CreatedAt: time.Now().Unix()}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		str, err := connstr.Order(ctx, deps.LN, *app.WalletPubkey, deps.Relays, secret, label)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrSignerUnavailable, err)
		}
		out = &Connection{AppID: app.ID, HubAppID: hub.ID, Label: label, WalletPubkey: *app.WalletPubkey,
			CreatedAt: row.CreatedAt, ConnectionString: str}
		created = app
		return nil
	})
	if err != nil {
		return nil, err
	}
	deps.Apps.NotifyAppCreated(created)
	return out, nil
}

// ListConnections lists a Cash Hub's Order connections, oldest first, each with
// its string rebuilt (left empty when the node cannot sign now).
func ListConnections(ctx context.Context, deps Deps, hubID uint) ([]Connection, error) {
	var rows []db.CashOrderConnection
	if err := deps.DB.Preload("App").Where("hub_app_id = ?", hubID).Order("app_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Connection, 0, len(rows))
	for _, r := range rows {
		c := Connection{AppID: r.AppID, HubAppID: r.HubAppID, Label: r.Label, RetiredAt: r.RetiredAt, CreatedAt: r.CreatedAt}
		if r.App.WalletPubkey != nil {
			c.WalletPubkey = *r.App.WalletPubkey
			if secret, err := deps.Keys.GetCashPairingKey(r.AppID); err == nil {
				if str, err := connstr.Order(ctx, deps.LN, c.WalletPubkey, deps.Relays, secret, r.Label); err == nil {
					c.ConnectionString = str
				}
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// RetireConnection retires an Order connection for good: from the commit on it
// refuses new orders and answers get_info with an empty cash_order object, while
// its orders are still served and minted. Retiring a retired one changes nothing.
func RetireConnection(deps Deps, hubID, connAppID uint, now time.Time) error {
	res := deps.DB.Model(&db.CashOrderConnection{}).
		Where("app_id = ? AND hub_app_id = ? AND retired_at IS NULL", connAppID, hubID).
		Update("retired_at", now.Unix())
	if res.Error != nil {
		return res.Error
	}
	invalidateInfo(deps.DB, "app_id = ?", connAppID)
	if res.RowsAffected == 0 {
		var n int64
		if err := deps.DB.Model(&db.CashOrderConnection{}).Where("app_id = ? AND hub_app_id = ?", connAppID, hubID).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: no order connection %d on this Cash Hub", constants.ErrInvalidParams, connAppID)
		}
	}
	return nil
}

// ConnectionOf is the Order connection row of an app, nil when it is none.
func ConnectionOf(gormDB *gorm.DB, appID uint) (*db.CashOrderConnection, error) {
	var row db.CashOrderConnection
	err := gormDB.Where("app_id = ?", appID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// invalidateInfo drops the cached get_info of the Order connections matching
// where, so a retirement or new terms are never answered stale.
func invalidateInfo(gormDB *gorm.DB, where string, args ...any) {
	var ids []uint
	if err := gormDB.Model(&db.CashOrderConnection{}).Where(where, args...).Pluck("app_id", &ids).Error; err != nil || len(ids) == 0 {
		return
	}
	var keys []string
	if err := gormDB.Model(&db.App{}).Where("id IN ?", ids).Pluck("app_pubkey", &keys).Error; err != nil {
		return
	}
	getinfocache.Invalidate(keys...)
}
