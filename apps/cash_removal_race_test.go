package apps_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// raceRemoval runs a recipient removal and a claim on one fresh slice per
// round, removal first, claim first or both at once, and requires exactly one
// winner each round, the loser refused as already claimed.
func raceRemoval(t *testing.T, claim func(svc *tests.TestService, c db.CashWalletClaim) error) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCashHub(t, svc, 10_000_000, 3600)
	wallet := newCashWallet(t, svc, hub)

	const rounds = 9
	removals, claims := 0, 0
	for round := range rounds {
		pubkey := randomHex32()
		require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pubkey, AmountMloki: 1000},
		}))
		var c db.CashWalletClaim
		require.NoError(t, svc.DB.Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, pubkey).First(&c).Error)

		var removeErr, claimErr error
		remove := func() { _, removeErr = svc.AppsService.DeleteCashClaim(wallet.ID, c.ID, nil) }
		take := func() { claimErr = claim(svc, c) }
		switch round % 3 {
		case 0:
			remove()
			take()
		case 1:
			take()
			remove()
		default:
			var wg sync.WaitGroup
			start := make(chan struct{})
			wg.Add(2)
			go func() { defer wg.Done(); <-start; remove() }()
			go func() { defer wg.Done(); <-start; take() }()
			close(start)
			wg.Wait()
		}

		require.True(t, (removeErr == nil) != (claimErr == nil), "exactly one winner: remove=%v claim=%v", removeErr, claimErr)
		if removeErr == nil {
			removals++
			assert.ErrorIs(t, claimErr, apps.ErrSliceClaimed)
		} else {
			claims++
			assert.ErrorIs(t, removeErr, constants.ErrInvalidParams)
			var after db.CashWalletClaim
			require.NoError(t, svc.DB.First(&after, c.ID).Error, "a claimed slice is never removed")
			assert.NotNil(t, after.ClaimedAt)
		}
	}
	t.Logf("removal won %d, claim won %d of %d", removals, claims, rounds)
}

// G5: removing a recipient while their cash_redeem claims the slice: first wins.
func TestRemovalRacesRedeemClaim_ExactlyOneWinner(t *testing.T) {
	raceRemoval(t, func(svc *tests.TestService, c db.CashWalletClaim) error {
		return svc.AppsService.ClaimSliceForRedeem(apps.RedeemClaim{
			Slice:       apps.SliceRef{ClaimID: c.ID, IdentityType: c.IdentityType, IdentityValue: c.IdentityValue, TransferCount: c.TransferCount},
			PaymentHash: randomHex32(),
		}, time.Now())
	})
}

// G5: removing a recipient while a transfer or consolidate claims the slice
// (the claim cashwallet's commit makes): first wins.
func TestRemovalRacesTransferClaim_ExactlyOneWinner(t *testing.T) {
	raceRemoval(t, func(svc *tests.TestService, c db.CashWalletClaim) error {
		return svc.DB.Transaction(func(tx *gorm.DB) error {
			return apps.ClaimCashSliceTx(tx, apps.SliceRef{ClaimID: c.ID, IdentityType: c.IdentityType,
				IdentityValue: c.IdentityValue, TransferCount: c.TransferCount}, nil, db.CashSliceStatusTransferred, time.Now())
		})
	})
}
