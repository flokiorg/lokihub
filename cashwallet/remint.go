package cashwallet

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
)

// RemintBill is the bill a cancelled on-chain withdrawal re-mints (NIP-CASH
// §Cancelling a Payout): one slice of the whole amount, bound to the slice's
// registered identity or, for a cash-mode slice, to the call's
// remint_commitment, with the slice's fee terms and expiry.
type RemintBill struct {
	Hub                 *db.App
	NameSeed            string
	AmountMillis        uint64
	ExpiresAt           *time.Time
	IdentityType        string
	IdentityValue       string
	IAPubkey            string
	MinTransferMillis   int64
	RedeemFeePpm        int
	RedeemFeeBaseMillis int64
}

// RemintedBill is a re-minted bill as created: its app, its issued token and
// the record row a lookup rebuilds the token from.
type RemintedBill struct {
	App    *db.App
	Token  string
	Record db.CashRequestRecordBill
}

// CreateRemintedBillTx creates a re-minted bill inside the caller's transaction
// tx: the app and its derived pairing key, the token signed by the node inside
// the open transaction, its one slice, and the stored token. It moves no value
// (the caller credits the bill from the withdrawal's hold) and announces
// nothing: the caller calls NotifyAppCreated after its own COMMIT.
func CreateRemintedBillTx(ctx context.Context, tx *gorm.DB, deps Deps, b RemintBill) (*RemintedBill, error) {
	if b.Hub == nil || b.AmountMillis == 0 {
		return nil, fmt.Errorf("a re-minted bill needs its hub and a positive amount")
	}
	nb := newBill{
		role:             db.CashRecordBillSingle,
		nameSeed:         b.NameSeed,
		amount:           b.AmountMillis,
		expiresAt:        b.ExpiresAt,
		identityRequired: b.IdentityType != db.CashIdentityCash,
	}
	app, secret, err := createBillAppTx(tx, deps, b.Hub, nb)
	if err != nil {
		return nil, err
	}
	identityRequired := nb.identityRequired
	tok, encoded, err := issueCashToken(ctx, deps.LNClient, *app.WalletPubkey, secret, deps.RelayURLs, &identityRequired, b.AmountMillis, b.Hub, nil)
	if err != nil {
		return nil, fmt.Errorf("refusing to re-mint a bill without a signed token: %w", err)
	}
	claim := db.CashWalletClaim{
		IdentityType:       b.IdentityType,
		IdentityValue:      b.IdentityValue,
		IAPubkey:           b.IAPubkey,
		AmountMloki:        int64(b.AmountMillis), //nolint:gosec // bounded by the slice it replaces
		MinTransferMloki:   b.MinTransferMillis,
		RedeemFeePpm:       b.RedeemFeePpm,
		RedeemFeeBaseMloki: b.RedeemFeeBaseMillis,
	}
	if err := deps.AppsService.CreateCashWalletClaimsTx(tx, app.ID, []db.CashWalletClaim{claim}); err != nil {
		return nil, fmt.Errorf("store the re-minted bill's slice: %w", err)
	}
	if err := tx.Model(&db.App{}).Where("id = ?", app.ID).Update("cash_token", encoded).Error; err != nil {
		return nil, fmt.Errorf("store the re-minted bill's token: %w", err)
	}
	app.CashToken = encoded
	return &RemintedBill{App: app, Token: encoded, Record: recordBill(nb.role, app, tok, nil)}, nil
}
