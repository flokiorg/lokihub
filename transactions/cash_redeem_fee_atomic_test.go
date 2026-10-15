package transactions

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Security Considerations: the redeem fee reconciliation MUST run
// atomically with payout settlement, not as a later, separate step
// (CASH-security-considerations-20). If the reconciliation cannot be written,
// the settlement must not be written either, rather than leaving a settled
// payout whose fee was never squared with the Hub.
func TestSendPaymentSync_CashRedeemFee_ReconciliationFailure_SettlementRollsBack(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const net = uint64(123_000) // tests.MockInvoice's fixed amount
	const quotedFee = uint64(500)
	_, wallet := newCashHubAndWallet(t, svc, net+quotedFee)

	injected := errors.New("injected: reconciliation row refused")
	require.NoError(t, svc.DB.Callback().Create().Before("gorm:create").Register("test:refuse_fee_reconciliation", func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*db.Transaction); ok && row.Description == "Cash redeem fee reconciliation" {
			_ = tx.AddError(injected)
		}
	}))
	t.Cleanup(func() { _ = svc.DB.Callback().Create().Remove("test:refuse_fee_reconciliation") })

	_, err = NewTransactionsService(svc.DB, svc.EventPublisher).SendPaymentSync(
		tests.MockInvoice, nil, redeemMetadata(quotedFee), svc.LNClient, &wallet.ID, nil,
	)
	require.Error(t, err, "a settlement whose fee reconciliation failed must not report success")

	var settled int64
	require.NoError(t, svc.DB.Model(&db.Transaction{}).
		Where("app_id = ? AND type = ? AND state = ?", wallet.ID, constants.TRANSACTION_TYPE_OUTGOING, constants.TRANSACTION_STATE_SETTLED).
		Count(&settled).Error)
	assert.Zero(t, settled, "the payout must not be recorded as settled without its reconciliation")
}
