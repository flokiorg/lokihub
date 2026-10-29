package cashwallet

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/ledger"
)

// OrderedBill is one bill a paid order mints: its amount and its single slice
// (the order's recipient and the bill terms frozen at placement).
type OrderedBill struct {
	Amount uint64
	Claim  db.CashWalletClaim
}

// MintedBill is a bill MintOrderedTx created: its app, its token and its record
// row (role ordered), which the caller completes and stores.
type MintedBill struct {
	App    *db.App
	Token  string
	Record db.CashRequestRecordBill
}

// MintOrderedTx creates and funds the bills of one order payment inside tx, as
// mint_cash does (NIP-CO §Minting): each bill's app and pairing key, its token
// signed first, its slice (ordered, no expiry), and its funding from the Hub.
// The caller has credited the Hub with the payment earlier in tx and locked the
// Hub; it announces each bill after COMMIT (deps.AppsService.NotifyAppCreated).
func MintOrderedTx(ctx context.Context, tx *gorm.DB, deps Deps, hub *db.App, bills []OrderedBill) ([]MintedBill, error) {
	out := make([]MintedBill, 0, len(bills))
	phase, err := newSignPhase(ctx, deps.LNClient, len(bills))
	if err != nil {
		return nil, fmt.Errorf("refusing to issue a bill without a signed token: %w", err)
	}
	defer phase.done()
	for _, b := range bills {
		claim := b.Claim
		claim.Ordered = true
		claim.AmountMloki = int64(b.Amount) //nolint:gosec // at most the order's max_millis
		nb := newBill{role: db.CashRecordBillOrdered, nameSeed: claim.IdentityValue, amount: b.Amount,
			claims: []db.CashWalletClaim{claim}, identityRequired: claim.IdentityType != db.CashIdentityCash}
		app, secret, err := createBillAppTx(tx, deps, hub, nb)
		if err != nil {
			return nil, err
		}
		identityRequired := nb.identityRequired
		tok, encoded, err := phase.issue(*app.WalletPubkey, secret, deps.RelayURLs, &identityRequired, b.Amount, hub, nil)
		if err != nil {
			return nil, fmt.Errorf("refusing to issue a bill without a signed token: %w", err)
		}
		if err := deps.AppsService.CreateCashWalletClaimsTx(tx, app.ID, nb.claims); err != nil {
			return nil, fmt.Errorf("store the bill's slice: %w", err)
		}
		if err := ledger.InternalTransferTx(tx, hub.ID, app.ID, b.Amount, "cash order"); err != nil {
			return nil, err
		}
		if err := tx.Model(&db.App{}).Where("id = ?", app.ID).Update("cash_token", encoded).Error; err != nil {
			return nil, fmt.Errorf("store the bill's token: %w", err)
		}
		app.CashToken = encoded
		out = append(out, MintedBill{App: app, Token: encoded, Record: recordBill(db.CashRecordBillOrdered, app, tok, nil)})
	}
	return out, nil
}
