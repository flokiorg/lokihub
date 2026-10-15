package api

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/tests/mocks"
	"github.com/flokiorg/lokihub/transactions"
)

// sentNotRecordedTxService pays for real, then reports the payment as sent but
// not recorded — what SendPaymentSync returns when the node's success response
// can't be recorded.
type sentNotRecordedTxService struct {
	transactions.TransactionsService
}

func (s sentNotRecordedTxService) SendPaymentSync(payReq string, amountMloki *uint64, metadata map[string]interface{}, lnClient lnclient.LNClient, appId *uint, requestEventId *uint) (*transactions.Transaction, error) {
	if _, err := s.TransactionsService.SendPaymentSync(payReq, amountMloki, metadata, lnClient, appId, requestEventId); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: no preimage in payment", transactions.ErrPaymentSentNotRecorded)
}

// A removed claim's sweep that went out but could not be recorded has moved the
// slice's funds to the hub. Keeping the slice would let a retry sweep the same
// amount again, out of the other recipient's share.
func TestDeleteCashClaim_SweepSentNotRecorded_ClaimRemoved(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).Pubkey = selfPaymentPubkey

	hub := tests.CreateCashHub(t, svc, 300_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 200)
	pkRemoved, pkRemaining := tests.RandomHex32(), tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pkRemoved, AmountMloki: 100_000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pkRemaining, AmountMloki: 100_000},
	}))
	tests.FundApp(svc, wallet.ID, 200_000, "fund-wallet")

	var claim db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, pkRemoved).First(&claim).Error)

	mockSvc := mocks.NewMockService(t)
	mockSvc.On("GetTransactionsService").Return(sentNotRecordedTxService{transactions.NewTransactionsService(svc.DB, svc.EventPublisher)}).Maybe()
	mockSvc.On("GetLNClient").Return(svc.LNClient).Maybe()
	theAPI := newTestAPIWithService(t, svc)
	theAPI.svc = mockSvc

	require.NoError(t, theAPI.DeleteCashClaim(hub.ID, wallet.ID, claim.ID), "a sweep that went out is not a failed removal")

	var removedCount int64
	svc.DB.Model(&db.CashWalletClaim{}).Where("id = ?", claim.ID).Count(&removedCount)
	assert.Zero(t, removedCount, "the swept slice is gone, so it cannot be swept a second time")

	var remainingCount int64
	svc.DB.Model(&db.CashWalletClaim{}).Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, pkRemaining).Count(&remainingCount)
	assert.EqualValues(t, 1, remainingCount, "the other recipient's slice is untouched")
}
