package apps_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// redeemableSlice mints one unclaimed 1000-mloki pubkey slice on a fresh wallet of hub.
func redeemableSlice(t *testing.T, svc *tests.TestService, hub *db.App) (*db.App, db.CashWalletClaim) {
	t.Helper()
	wallet := newCashWallet(t, svc, hub)
	pubkey := randomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pubkey, AmountMloki: 1000},
	}))
	var c db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, pubkey).First(&c).Error)
	return wallet, c
}

func claimForRedeem(svc *tests.TestService, c db.CashWalletClaim, hash string, transferCount int) error {
	return svc.AppsService.ClaimSliceForRedeem(apps.RedeemClaim{
		Slice:       apps.SliceRef{ClaimID: c.ID, IdentityType: c.IdentityType, IdentityValue: c.IdentityValue, TransferCount: transferCount},
		PaymentHash: hash,
	}, time.Now())
}

func TestClaimSliceForRedeem_SecondClaimIsRefused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	_, c := redeemableSlice(t, svc, newCashHub(t, svc, 10_000_000, 3600))

	require.NoError(t, claimForRedeem(svc, c, randomHex32(), c.TransferCount), "the first claim wins")
	assert.ErrorIs(t, claimForRedeem(svc, c, randomHex32(), c.TransferCount), apps.ErrSliceClaimed, "a second claim with another invoice")
}

func TestClaimSliceForRedeem_ConcurrentClaimsHaveExactlyOneWinner(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCashHub(t, svc, 10_000_000, 3600)
	for trial := 0; trial < raceTrials(20); trial++ {
		_, c := redeemableSlice(t, svc, hub)
		errs := make([]error, 8)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range errs {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; errs[i] = claimForRedeem(svc, c, randomHex32(), c.TransferCount) }()
		}
		close(start)
		wg.Wait()
		won := 0
		for _, e := range errs {
			if e == nil {
				won++
			} else {
				assert.ErrorIs(t, e, apps.ErrSliceClaimed)
			}
		}
		require.Equal(t, 1, won, "trial %d: exactly one concurrent claim wins: %v", trial, errs)
	}
}

func TestClaimSliceForRedeem_StaleVersionIsRefused(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	_, c := redeemableSlice(t, svc, newCashHub(t, svc, 10_000_000, 3600))

	assert.ErrorIs(t, claimForRedeem(svc, c, randomHex32(), c.TransferCount+1), apps.ErrSliceClaimed, "a version a transfer has since changed")
	var after db.CashWalletClaim
	require.NoError(t, svc.DB.First(&after, c.ID).Error)
	assert.Nil(t, after.ClaimedAt, "the stale claim wrote nothing")
	assert.NoError(t, claimForRedeem(svc, c, randomHex32(), c.TransferCount), "the current version still claims")
}

func TestReleaseRedeemClaim_KeepsAPendingOrSettledPayoutClaimed(t *testing.T) {
	for _, state := range []string{constants.TRANSACTION_STATE_PENDING, constants.TRANSACTION_STATE_SETTLED} {
		t.Run(state, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()
			wallet, c := redeemableSlice(t, svc, newCashHub(t, svc, 10_000_000, 3600))
			hash := randomHex32()
			require.NoError(t, claimForRedeem(svc, c, hash, c.TransferCount))
			require.NoError(t, svc.DB.Create(&db.Transaction{AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_OUTGOING,
				State: state, AmountMloki: 1000, PaymentHash: hash}).Error)

			released, err := svc.AppsService.ReleaseRedeemClaim(wallet.ID, hash, nil)
			require.NoError(t, err)
			assert.False(t, released)
			var after db.CashWalletClaim
			require.NoError(t, svc.DB.First(&after, c.ID).Error)
			assert.NotNil(t, after.ClaimedAt, "the slice stays claimed")
			assert.ErrorIs(t, claimForRedeem(svc, after, randomHex32(), after.TransferCount), apps.ErrSliceClaimed)
		})
	}
}

func TestReleaseRedeemClaim_ReopensAFailedPayout(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	wallet, c := redeemableSlice(t, svc, newCashHub(t, svc, 10_000_000, 3600))
	hash := randomHex32()
	require.NoError(t, claimForRedeem(svc, c, hash, c.TransferCount))
	require.NoError(t, svc.DB.Create(&db.Transaction{AppId: &wallet.ID, Type: constants.TRANSACTION_TYPE_OUTGOING,
		State: constants.TRANSACTION_STATE_FAILED, AmountMloki: 1000, PaymentHash: hash}).Error)

	released, err := svc.AppsService.ReleaseRedeemClaim(wallet.ID, hash, nil)
	require.NoError(t, err)
	assert.True(t, released)
	assert.NoError(t, claimForRedeem(svc, c, randomHex32(), c.TransferCount), "a failed payout's slice is redeemable again")
}

func TestClaimSliceForRedeem_NodeInflightBoundIsExact(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCashHub(t, svc, 10_000_000, 3600)
	claim := func(c db.CashWalletClaim) error {
		return svc.AppsService.ClaimSliceForRedeem(apps.RedeemClaim{
			Slice:       apps.SliceRef{ClaimID: c.ID, IdentityType: c.IdentityType, IdentityValue: c.IdentityValue, TransferCount: c.TransferCount},
			PaymentHash: randomHex32(), InflightNode: 2,
		}, time.Now())
	}
	_, a := redeemableSlice(t, svc, hub)
	_, b := redeemableSlice(t, svc, hub)
	_, c := redeemableSlice(t, svc, hub)
	require.NoError(t, claim(a), "0 in flight, bound 2")
	require.NoError(t, claim(b), "1 in flight, bound 2")
	assert.ErrorIs(t, claim(c), apps.ErrRedeemInflight, "2 in flight, bound 2")
	var after db.CashWalletClaim
	require.NoError(t, svc.DB.First(&after, c.ID).Error)
	assert.Nil(t, after.ClaimedAt, "refused before the claim")
}

// One payee takes at most InflightPerPayee of a Hub's payouts in flight; other
// payees and same-node payouts still get the Hub's remaining slots, and a
// released payout gives its payee's slot back.
func TestClaimSliceForRedeem_PerPayeeInflightBound(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := newCashHub(t, svc, 10_000_000, 3600)
	other := newCashHub(t, svc, 10_000_000, 3600)
	holder, honest := "02"+randomHex32(), "03"+randomHex32()
	claim := func(h *db.App, c db.CashWalletClaim, payee string) (string, error) {
		hash := randomHex32()
		return hash, svc.AppsService.ClaimSliceForRedeem(apps.RedeemClaim{
			Slice:       apps.SliceRef{ClaimID: c.ID, IdentityType: c.IdentityType, IdentityValue: c.IdentityValue, TransferCount: c.TransferCount},
			HubAppID:    h.ID, PaymentHash: hash, InflightPerHub: 32, Payee: payee, InflightPerPayee: 2,
		}, time.Now())
	}
	w1, s1 := redeemableSlice(t, svc, hub)
	_, s2 := redeemableSlice(t, svc, hub)
	_, s3 := redeemableSlice(t, svc, hub)
	_, err = claim(hub, s1, holder)
	require.NoError(t, err, "0 to this payee, bound 2")
	_, err = claim(hub, s2, holder)
	require.NoError(t, err, "1 to this payee, bound 2")
	_, err = claim(hub, s3, holder)
	assert.ErrorIs(t, err, apps.ErrRedeemInflight, "2 to this payee, bound 2")
	var after db.CashWalletClaim
	require.NoError(t, svc.DB.First(&after, s3.ID).Error)
	assert.Nil(t, after.ClaimedAt, "refused before the claim")

	_, s4 := redeemableSlice(t, svc, hub)
	_, err = claim(hub, s4, honest)
	assert.NoError(t, err, "another payee keeps the Hub's slots")
	_, s5 := redeemableSlice(t, svc, hub)
	_, err = claim(hub, s5, "")
	assert.NoError(t, err, "a same-node payout is not counted per payee")
	_, s6 := redeemableSlice(t, svc, other)
	_, err = claim(other, s6, holder)
	assert.NoError(t, err, "the bound is per Hub")

	// Releasing a failed payout frees its payee's slot.
	var c1 db.CashWalletClaim
	require.NoError(t, svc.DB.First(&c1, s1.ID).Error)
	released, err := svc.AppsService.ReleaseRedeemClaim(w1.ID, c1.PaymentHash, nil)
	require.NoError(t, err)
	require.True(t, released)
	require.NoError(t, svc.DB.First(&c1, s1.ID).Error)
	assert.Empty(t, c1.RedeemPayee)
	_, s7 := redeemableSlice(t, svc, hub)
	_, err = claim(hub, s7, holder)
	assert.NoError(t, err, "a released payout gives its payee's slot back")
}
