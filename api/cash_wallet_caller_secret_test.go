package api

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Minting Cash: a caller-supplied cash_secret MUST be rejected, on the
// admin HTTP mint as on mint_cash.
func TestCreateCashWallet_CallerSuppliedCashSecret_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	result, err := newTestAPIWithService(t, svc).CreateCashWallet(hub.ID, &CreateCashWalletRequest{
		Recipients: []CashWalletRecipient{
			{IdentityType: db.CashIdentityCash, AmountMloki: 1000, CashSecret: strings.Repeat("11", 32)},
		},
		ExpirySecs: 1800,
	})
	require.ErrorIs(t, err, constants.ErrInvalidParams)
	assert.Nil(t, result)

	var bills int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("kind = ?", db.AppKindCashWallet).Count(&bills).Error)
	assert.Zero(t, bills, "nothing is minted")
}
