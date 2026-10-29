package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashwithdraw"
	"github.com/flokiorg/lokihub/cashwithdraw/withdrawtest"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests/mocks"
)

// guardFunds is a payout engine whose spend guard admits at most max sat.
type guardFunds struct {
	withdrawtest.Funds
	max uint64
}

func (g *guardFunds) SpendCheck(_ context.Context, amountSat uint64, sendAll bool) error {
	if sendAll || amountSat > g.max {
		return cashwithdraw.ErrSpendSetAside
	}
	return nil
}

type withdrawSvc struct {
	*mocks.MockService
	w *cashwithdraw.Service
}

func (s withdrawSvc) GetWithdrawService() *cashwithdraw.Service { return s.w }

// R2-P-M3: opening a channel is an on-chain spend; it is refused when it would
// take coins set aside for withdrawals, as RedeemOnchainFunds is.
func TestOpenChannelRefusedIntoSetAside(t *testing.T) {
	svc := mocks.NewMockService(t)
	ln := mocks.NewMockLNClient(t)
	svc.On("GetLNClient").Return(ln)
	theAPI := &api{svc: withdrawSvc{MockService: svc, w: &cashwithdraw.Service{Funds: &guardFunds{max: 50_000}}}}

	_, err := theAPI.OpenChannel(context.TODO(), &OpenChannelRequest{Pubkey: "02aa", AmountLoki: 60_000})
	require.ErrorIs(t, err, cashwithdraw.ErrSpendSetAside)
	ln.AssertNotCalled(t, "OpenChannel", mock.Anything, mock.Anything)

	ln.On("OpenChannel", mock.Anything, mock.Anything).Return(&lnclient.OpenChannelResponse{FundingTxId: "f"}, nil)
	res, err := theAPI.OpenChannel(context.TODO(), &OpenChannelRequest{Pubkey: "02aa", AmountLoki: 40_000})
	require.NoError(t, err)
	require.Equal(t, "f", res.FundingTxId)
}
