package cashwallet

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
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

// A split's funding transfer that went out but could not be recorded has moved
// the funds. Treating it as failed abandoned (deleted) the funded split-off bill
// and told the caller the source was intact, so its claim would be restored
// while the value also sat in the new bill.
func TestSplit_FundingSentNotRecorded_IsAFundedSplit(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	src := newSplitSource(t, svc, hub)
	srcBefore := queries.GetIsolatedBalance(svc.DB, src.ID)

	deps := newTestDeps(svc)
	deps.TransactionsService = sentNotRecordedTxService{deps.TransactionsService}

	exp := time.Now().Add(time.Hour)
	res, err := Split(context.TODO(), deps, SplitParams{
		HubApp: hub, SourceWalletApp: src, AmountMloki: 1000,
		NewIdentityType: db.CashIdentityCash, NewIdentityValue: strings.Repeat("ef", 32),
		ExpiresAt: &exp,
	})
	require.NoError(t, err, "a transfer that went out is a completed split")
	require.NotNil(t, res)
	require.NotEmpty(t, res.CashToken)

	var bill db.App
	require.NoError(t, svc.DB.First(&bill, res.WalletApp.ID).Error, "the funded bill is not abandoned")
	assert.Less(t, queries.GetIsolatedBalance(svc.DB, src.ID), srcBefore, "the source paid")
	assert.Equal(t, res.CashToken, bill.CashToken, "and the bill's token is stored like any funded split's")
}
