package cashwallet

import (
	"context"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/db"
)

// SplitParams is a full spin-off of a fresh pubkey slice of AmountMloki on
// SourceWalletApp into one new bill for the target, the shape a full
// cash_transfer split takes.
type SplitParams struct {
	HubApp           *db.App
	SourceWalletApp  *db.App
	AmountMloki      uint64
	NewIdentityType  string
	NewIdentityValue string
	NewIAPubkey      string
	MinTransferMloki int64
	ExpiresAt        *time.Time
}

// Split registers the source slice SplitParams describes and spins it off whole.
func Split(ctx context.Context, deps Deps, p SplitParams) (*SplitResult, error) {
	ref, err := addSlice(deps, p.SourceWalletApp, p.AmountMloki, p.MinTransferMloki)
	if err != nil {
		return nil, err
	}
	res, err := SplitInTwo(ctx, deps, SplitInTwoParams{
		HubApp:              p.HubApp,
		SourceWalletApp:     p.SourceWalletApp,
		Source:              ref,
		CarvedIdentityType:  p.NewIdentityType,
		CarvedIdentityValue: p.NewIdentityValue,
		CarvedIAPubkey:      p.NewIAPubkey,
		CarvedAmountMloki:   p.AmountMloki,
		MinTransferMloki:    p.MinTransferMloki,
		ExpiresAt:           p.ExpiresAt,
	})
	if err != nil {
		return nil, err
	}
	return res.Carved, nil
}

// addSlice registers an unclaimed pubkey slice of amount on wallet.
func addSlice(deps Deps, wallet *db.App, amount uint64, minTransfer int64) (apps.SliceRef, error) {
	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	claim := db.CashWalletClaim{
		WalletAppID: wallet.ID, IdentityType: db.CashIdentityPubkey, IdentityValue: pk,
		AmountMloki: int64(amount), MinTransferMloki: minTransfer, //nolint:gosec // test amounts
	}
	if err := deps.DB.Create(&claim).Error; err != nil {
		return apps.SliceRef{}, err
	}
	return apps.SliceRef{ClaimID: claim.ID, IdentityType: claim.IdentityType, IdentityValue: claim.IdentityValue}, nil
}

func mustAddSlice(t *testing.T, deps Deps, wallet *db.App, amount uint64) apps.SliceRef {
	t.Helper()
	ref, err := addSlice(deps, wallet, amount, 0)
	require.NoError(t, err)
	return ref
}
