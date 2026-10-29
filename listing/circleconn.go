package listing

import (
	"fmt"

	"github.com/nbd-wtf/go-nostr"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// createCircleConnTx creates a listed circle's own connection (WP-25 item 6;
// H-CASH-462, H-CW-072): a circle_hub_conn child of hub sharing its wallet
// pubkey (TLV 0), with its own client key derived from the new row's id (TLV 2's
// secret is re-derived, never stored). It publishes no app event: no info event
// is published for it and no subscription is needed, since requests to the
// hub's wallet pubkey are already served.
func createCircleConnTx(tx *gorm.DB, keys PairingKeys, hub *db.App, title string) (*db.App, error) {
	if hub.WalletPubkey == nil {
		return nil, fmt.Errorf("the circle has no wallet pubkey")
	}
	name := "Listing connection"
	if title != "" {
		name += ": " + title
	}
	parent := hub.ID
	wallet := *hub.WalletPubkey
	conn := &db.App{
		Name:         name,
		Kind:         db.AppKindCircleHubConn,
		ParentAppID:  &parent,
		ParentKind:   db.ParentKindListing,
		WalletPubkey: &wallet,
		// A placeholder until the id is known; the unique index skips ''.
		AppPubkey: "",
	}
	if err := tx.Create(conn).Error; err != nil {
		return nil, fmt.Errorf("create the listing connection: %w", err)
	}
	secret, err := keys.GetCashPairingKey(conn.ID)
	if err != nil {
		return nil, fmt.Errorf("derive the listing connection's key: %w", err)
	}
	pub, err := nostr.GetPublicKey(secret)
	if err != nil {
		return nil, err
	}
	if err := tx.Model(conn).Update("app_pubkey", pub).Error; err != nil {
		return nil, fmt.Errorf("register the listing connection's key: %w", err)
	}
	conn.AppPubkey = pub
	return conn, nil
}

// deleteCircleConnTx deletes a listing connection: its client key resolves to no
// app from then on, so a request or bill proof signed by it gets no answer.
func deleteCircleConnTx(tx *gorm.DB, id uint) error {
	return tx.Where("id = ? AND kind = ?", id, db.AppKindCircleHubConn).Delete(&db.App{}).Error
}

// LiveCircleConnKeys are the client keys of the live listing connections of the
// circle whose wallet pubkey is walletPubkey (H-CW-072).
func LiveCircleConnKeys(tx *gorm.DB, walletPubkey string) ([]string, error) {
	var keys []string
	err := tx.Model(&db.App{}).Where("wallet_pubkey = ? AND kind = ?", walletPubkey, db.AppKindCircleHubConn).
		Pluck("app_pubkey", &keys).Error
	return keys, err
}
