package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// oneSliceBill is a funded Cash Hub bill with one unclaimed 200_000 slice.
func oneSliceBill(t *testing.T, svc *tests.TestService) (hub, wallet *db.App, claim db.CashWalletClaim) {
	t.Helper()
	hub, _, err := svc.AppsService.CreateApp("hub", "", 0, "never", nil,
		[]string{constants.GET_BALANCE_SCOPE}, db.AppKindCashHub, nil, "", nil)
	require.NoError(t, err)
	wallet = createSubWallet(t, svc, db.AppKindCashWallet, hub.ID, db.ParentKindCash, makeFutureTime())
	tests.FundApp(svc, wallet.ID, 200_000, tests.RandomHex32())
	claim = db.CashWalletClaim{WalletAppID: wallet.ID, IdentityType: db.CashIdentityPubkey,
		IdentityValue: tests.RandomHex32(), AmountMloki: 200_000}
	require.NoError(t, svc.DB.Create(&claim).Error)
	return hub, wallet, claim
}

func ownerDelete(svc *tests.TestService, wallet *db.App) error {
	return ReclaimAndDeleteSubWallet(context.Background(), svc.DB, transactions.NewTransactionsService(svc.DB, svc.EventPublisher),
		svc.LNClient, *wallet, SubWalletDeletion{Outcome: db.CashBillOutcomeDeleted})
}

func walletExists(t *testing.T, svc *tests.TestService, id uint) bool {
	var n int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", id).Count(&n).Error)
	return n == 1
}

// NIP-CASH §Lifecycle and Deletion (CASH-447, CASH-449): a wallet is never
// deleted while a cash_redeem payment from it is in flight, its claim won.
func TestOwnerDelete_RefusedWhileARedeemIsInFlight(t *testing.T) {
	for _, c := range []struct {
		name   string
		ledger string // the payout's ledger row state, "" for none yet
	}{
		{"claimed, payment not in the ledger yet", ""},
		{"payment pending", constants.TRANSACTION_STATE_PENDING},
		{"payment failed, slice not yet released", constants.TRANSACTION_STATE_FAILED},
	} {
		t.Run(c.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			hub, wallet, claim := oneSliceBill(t, svc)
			hash := tests.RandomHex32()
			require.NoError(t, svc.AppsService.ClaimSliceForRedeem(apps.RedeemClaim{Slice: apps.SliceRef{ClaimID: claim.ID,
				IdentityType: claim.IdentityType, IdentityValue: claim.IdentityValue}, PaymentHash: hash}, time.Now()))
			if c.ledger != "" {
				require.NoError(t, svc.DB.Create(&db.Transaction{AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_OUTGOING,
					State: c.ledger, AmountMloki: 200_000, PaymentHash: hash}).Error)
			}

			assert.ErrorIs(t, ownerDelete(svc, wallet), constants.ErrInvalidParams)
			assert.True(t, walletExists(t, svc, wallet.ID), "the wallet stands")
			assert.Zero(t, queries.GetIsolatedBalance(svc.DB, hub.ID), "nothing swept to the Hub")
		})
	}
}

func TestOwnerDelete_ProceedsOnceThePayoutSettledOrNeverStarted(t *testing.T) {
	for _, c := range []struct {
		name      string
		claimedAt time.Duration
		settled   bool
	}{
		{"payout settled", 0, true},
		{"claim older than the start window, no payment", -redeemStartWindow - time.Minute, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			_, wallet, claim := oneSliceBill(t, svc)
			hash := tests.RandomHex32()
			require.NoError(t, svc.AppsService.ClaimSliceForRedeem(apps.RedeemClaim{Slice: apps.SliceRef{ClaimID: claim.ID,
				IdentityType: claim.IdentityType, IdentityValue: claim.IdentityValue}, PaymentHash: hash}, time.Now().Add(c.claimedAt)))
			if c.settled {
				require.NoError(t, svc.DB.Create(&db.Transaction{AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_OUTGOING,
					State: constants.TRANSACTION_STATE_SETTLED, AmountMloki: 200_000, PaymentHash: hash}).Error)
			}
			require.NoError(t, ownerDelete(svc, wallet))
			assert.False(t, walletExists(t, svc, wallet.ID))
		})
	}
}

// The delete sweeps the balance and archives the unclaimed slice as
// reclaimed in one transaction.
func TestOwnerDelete_SweepsAndArchivesTheUnclaimedSlice(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub, wallet, claim := oneSliceBill(t, svc)

	require.NoError(t, ownerDelete(svc, wallet))
	assert.False(t, walletExists(t, svc, wallet.ID))
	assert.EqualValues(t, 200_000, queries.GetIsolatedBalance(svc.DB, hub.ID))
	var arch db.CashBillSliceArchive
	require.NoError(t, svc.DB.Where("claim_id = ?", claim.ID).First(&arch).Error)
	assert.Equal(t, db.CashSliceStatusReclaimed, arch.Outcome)
	var bill db.CashBillArchive
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", wallet.ID).First(&bill).Error)
	assert.EqualValues(t, 200_000, bill.ReclaimedMloki)
}

// An owner delete racing a cash_redeem claim of the same slice: exactly one
// wins. Either the redeem claims and the wallet stands with its balance, or
// the wallet is gone and the redeem is refused.
func TestOwnerDelete_RacingARedeemClaimHasOneWinner(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	trials := 40
	if raceDetectorEnabled {
		trials = 10
	}
	for i := 0; i < trials; i++ {
		hub, wallet, claim := oneSliceBill(t, svc)
		hubBefore := queries.GetIsolatedBalance(svc.DB, hub.ID)
		var claimErr, deleteErr error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			claimErr = svc.AppsService.ClaimSliceForRedeem(apps.RedeemClaim{Slice: apps.SliceRef{ClaimID: claim.ID,
				IdentityType: claim.IdentityType, IdentityValue: claim.IdentityValue}, PaymentHash: tests.RandomHex32()}, time.Now())
		}()
		go func() { defer wg.Done(); <-start; deleteErr = ownerDelete(svc, wallet) }()
		close(start)
		wg.Wait()

		exists := walletExists(t, svc, wallet.ID)
		if claimErr == nil {
			require.True(t, exists, "trial %d: the redeem won its claim, so the wallet stands", i)
			require.ErrorIs(t, deleteErr, constants.ErrInvalidParams, "trial %d", i)
			require.EqualValues(t, 200_000, queries.GetIsolatedBalance(svc.DB, wallet.ID), "trial %d: the payout stays funded", i)
			require.Equal(t, hubBefore, queries.GetIsolatedBalance(svc.DB, hub.ID), "trial %d", i)
		} else {
			require.ErrorIs(t, claimErr, apps.ErrSliceClaimed, "trial %d", i)
			require.NoError(t, deleteErr, "trial %d", i)
			require.False(t, exists, "trial %d", i)
			require.Equal(t, hubBefore+200_000, queries.GetIsolatedBalance(svc.DB, hub.ID), "trial %d", i)
		}
	}
}
