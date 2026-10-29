package nip47

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/circlelife"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CW §Deleting a Circle Wallet: a payment to the wallet still in flight
// refuses its deletion, whoever asks: a same-node keysend whose credit is not
// settled yet, or a held keysend the node settled and the Hub has not credited.

func TestDeleteCircleWallet_PendingSameNodeKeysendRefused(t *testing.T) {
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	w := c.member()
	require.NoError(t, c.svc.DB.Where("app_id = ?", w.app.ID).Delete(&db.Transaction{}).Error)
	// What SendKeysend writes for a same-node recipient before it settles the credit.
	preimage := tests.RandomHex32()
	credit := db.Transaction{AppId: &w.app.ID, Type: constants.TRANSACTION_TYPE_INCOMING,
		State: constants.TRANSACTION_STATE_PENDING, AmountMloki: 50_000, PaymentHash: tests.RandomHex32(),
		Preimage: &preimage, SelfPayment: true}
	require.NoError(t, c.svc.DB.Create(&credit).Error)

	resp := w.call(constants.NIP47MethodDeleteCircleWallet, map[string]interface{}{})
	assert.Equal(t, constants.ERROR_RESTRICTED, errorCode(resp), "a payment to the wallet is in flight")
	require.NotNil(t, c.svc.AppsService.GetAppById(w.app.ID))
	var stored db.Transaction
	require.NoError(t, c.svc.DB.First(&stored, credit.ID).Error)
	assert.Equal(t, constants.TRANSACTION_STATE_PENDING, stored.State, "the credit is left to settle")

	// Once the payer's keysend failed, the wallet holds nothing and goes.
	require.NoError(t, c.svc.DB.Model(&stored).Update("state", constants.TRANSACTION_STATE_FAILED).Error)
	resp = w.call(constants.NIP47MethodDeleteCircleWallet, map[string]interface{}{})
	require.Nil(t, resp.Error, "%+v", resp.Error)
	assert.Nil(t, c.svc.AppsService.GetAppById(w.app.ID))
}

func TestDeleteCircleWallet_OpenInvoiceDoesNotBlock(t *testing.T) {
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	w := c.member()
	require.NoError(t, c.svc.DB.Where("app_id = ?", w.app.ID).Delete(&db.Transaction{}).Error)
	expires := time.Now().Add(time.Hour)
	invoice := db.Transaction{AppId: &w.app.ID, Type: constants.TRANSACTION_TYPE_INCOMING,
		State: constants.TRANSACTION_STATE_PENDING, AmountMloki: 50_000, PaymentHash: tests.RandomHex32(), ExpiresAt: &expires}
	require.NoError(t, c.svc.DB.Create(&invoice).Error)

	resp := w.call(constants.NIP47MethodDeleteCircleWallet, map[string]interface{}{})
	require.Nil(t, resp.Error, "an unpaid invoice is cancelled, not in flight: %+v", resp.Error)
	assert.Nil(t, c.svc.AppsService.GetAppById(w.app.ID))
}

func TestDeleteCircleWallet_HeldKeysendNotYetCreditedRefused(t *testing.T) {
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	w := c.member()
	require.NoError(t, c.svc.DB.Where("app_id = ?", w.app.ID).Delete(&db.Transaction{}).Error)
	held := db.CircleHeldKeysend{PaymentHash: tests.RandomHex32(), WalletAppID: w.app.ID, HubAppID: c.hub.ID,
		AmountMloki: 50_000, ReservedAt: time.Now().Unix()}
	require.NoError(t, c.svc.DB.Create(&held).Error)

	resp := w.call(constants.NIP47MethodDeleteCircleWallet, map[string]interface{}{})
	assert.Equal(t, constants.ERROR_RESTRICTED, errorCode(resp), "a settled keysend is still to be credited")
	require.NotNil(t, c.svc.AppsService.GetAppById(w.app.ID))

	// A reservation past its lifetime is a lost credit, no longer in flight.
	require.NoError(t, c.svc.DB.Model(&held).Update("reserved_at", time.Now().Add(-circlelife.HeldKeysendTTL-time.Second).Unix()).Error)
	resp = w.call(constants.NIP47MethodDeleteCircleWallet, map[string]interface{}{})
	require.Nil(t, resp.Error, "%+v", resp.Error)
	assert.Nil(t, c.svc.AppsService.GetAppById(w.app.ID))
}

func TestDeleteCircleWallet_AnotherWalletsHeldKeysendDoesNotBlock(t *testing.T) {
	c := newFreezeCircle(t, db.CirclePolicyAllowlist)
	w, other := c.member(), c.member()
	require.NoError(t, c.svc.DB.Where("app_id = ?", w.app.ID).Delete(&db.Transaction{}).Error)
	require.NoError(t, c.svc.DB.Create(&db.CircleHeldKeysend{PaymentHash: tests.RandomHex32(), WalletAppID: other.app.ID,
		HubAppID: c.hub.ID, AmountMloki: 50_000, ReservedAt: time.Now().Unix()}).Error)

	resp := w.call(constants.NIP47MethodDeleteCircleWallet, map[string]interface{}{})
	require.Nil(t, resp.Error, "%+v", resp.Error)
	assert.Nil(t, c.svc.AppsService.GetAppById(w.app.ID))
}
