package nip47

import (
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func (c *freezeCircle) member() *memberWallet {
	c.t.Helper()
	member := nostr.GeneratePrivateKey()
	memberPub, _ := nostr.GetPublicKey(member)
	c.add(memberPub)
	return c.join(member)
}

// U-16.RD1: every NIP-47 method outside NIP-CW §What a Circle Wallet Connection
// Allows is refused RESTRICTED for an Active wallet, before any scope.
func TestCircleWalletGate_MethodsOutsideTheListRestricted(t *testing.T) {
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	w := c.member()
	for method, params := range map[string]map[string]interface{}{
		models.MAKE_HOLD_INVOICE_METHOD:   {"amount": 1000, "payment_hash": tests.RandomHex32()},
		models.SETTLE_HOLD_INVOICE_METHOD: {"preimage": tests.RandomHex32()},
		models.CANCEL_HOLD_INVOICE_METHOD: {"payment_hash": tests.RandomHex32()},
		models.MULTI_PAY_INVOICE_METHOD:   {"invoices": []map[string]interface{}{{"invoice": tests.MockInvoice}}},
		models.MULTI_PAY_KEYSEND_METHOD:   {"keysends": []map[string]interface{}{{"amount": 1000, "pubkey": "02" + strings.Repeat("ab", 32)}}},
		models.SIGN_MESSAGE_METHOD:        {"message": "hi"},
		models.CREATE_CONNECTION_METHOD:   {"pubkey": tests.RandomHex32()},
	} {
		resp := w.call(method, params)
		assert.Equal(t, constants.ERROR_RESTRICTED, errorCode(resp), "%s: %+v", method, resp.Error)
	}
	var rows int64
	require.NoError(t, c.svc.DB.Model(&db.Transaction{}).Where("app_id = ? AND type = ?", w.app.ID, constants.TRANSACTION_TYPE_OUTGOING).Count(&rows).Error)
	assert.Zero(t, rows, "nothing was paid")
}

// A wallet that is not Active only pays out, reads and deletes itself (NIP-CW
// §What a Circle Wallet Connection Allows).
func TestCircleWalletGate_ClosingWalletPaysOutOnly(t *testing.T) {
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	w := c.member()
	require.NoError(t, c.svc.DB.Model(&db.App{}).Where("id = ?", w.app.ID).Update("circle_state", circlelife.StateClosing).Error)

	for _, method := range []string{models.MAKE_INVOICE_METHOD, models.GET_BUDGET_METHOD} {
		resp := w.call(method, map[string]interface{}{"amount": 1000})
		assert.Equal(t, constants.ERROR_RESTRICTED, errorCode(resp), "%s on a Closing wallet: %+v", method, resp.Error)
	}
	balance := w.call(models.GET_BALANCE_METHOD, nil)
	require.Nil(t, balance.Error, "%+v", balance.Error)
	pay := w.call(models.PAY_INVOICE_METHOD, map[string]interface{}{"invoice": tests.MockInvoice})
	assert.Nil(t, pay.Error, "a Closing wallet still pays out: %+v", pay.Error)
}

// NIP-CW §Deleting a Circle Wallet: refused RESTRICTED while the wallet holds
// more than dust; served once it holds none, deleting it in one step; never
// served on another connection (U-16.8).
func TestDeleteCircleWallet(t *testing.T) {
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	w := c.member()

	resp := w.call(constants.NIP47MethodDeleteCircleWallet, map[string]interface{}{})
	assert.Equal(t, constants.ERROR_RESTRICTED, errorCode(resp), "1,000,000 mloki is more than dust")

	// No other connection can delete it for the member: not the Hub's, not the
	// host's.
	for _, other := range []*db.App{c.hub, {Kind: db.AppKindStandard}, {Kind: db.AppKindCashWallet}} {
		ok, code, _ := circleWalletGate(other, constants.NIP47MethodDeleteCircleWallet, time.Now())
		assert.False(t, ok)
		assert.Equal(t, constants.ERROR_RESTRICTED, code)
	}

	// Paid out down to dust (the Hub's default threshold, 1,000 mloki).
	require.NoError(t, c.svc.DB.Where("app_id = ?", w.app.ID).Delete(&db.Transaction{}).Error)
	tests.FundApp(c.svc, w.app.ID, 1_000, tests.RandomHex32())

	resp = w.call(constants.NIP47MethodDeleteCircleWallet, map[string]interface{}{})
	require.Nil(t, resp.Error, "%+v", resp.Error)
	assert.Nil(t, c.svc.AppsService.GetAppById(w.app.ID), "deleted at once")
	assert.Equal(t, int64(1_000)+int64(100_000_000), queries.GetIsolatedBalance(c.svc.DB, c.hub.ID), "the dust stays with the Hub")
	var memberships int64
	require.NoError(t, c.svc.DB.Model(&db.CircleWalletMembership{}).Where("wallet_app_id = ?", w.app.ID).Count(&memberships).Error)
	assert.Zero(t, memberships, "the member may join again")
	var record db.CircleJoinRecord
	require.NoError(t, c.svc.DB.Where("wallet_app_id = ?", w.app.ID).First(&record).Error)
	assert.NotNil(t, record.WalletDeletedAt)
}

// A payment in flight refuses the deletion whatever the balance.
func TestDeleteCircleWallet_InFlightRefused(t *testing.T) {
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	w := c.member()
	require.NoError(t, c.svc.DB.Where("app_id = ?", w.app.ID).Delete(&db.Transaction{}).Error)
	require.NoError(t, c.svc.DB.Create(&db.Transaction{AppId: &w.app.ID, Type: constants.TRANSACTION_TYPE_INCOMING,
		State: constants.TRANSACTION_STATE_ACCEPTED, AmountMloki: 500, PaymentHash: tests.RandomHex32(), Hold: true}).Error)

	resp := w.call(constants.NIP47MethodDeleteCircleWallet, map[string]interface{}{})
	assert.Equal(t, constants.ERROR_RESTRICTED, errorCode(resp))
	assert.NotNil(t, c.svc.AppsService.GetAppById(w.app.ID))
}
