package controllers

// Test coverage for list_recipients' new redeem_fee_mloki/net_redeemable_mloki
// fields (NIP-CASH.md §Listing Recipients) — the quote a recipient uses to
// know exactly what cash_redeem will pay out before calling it.

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

func TestHandleListRecipientsEvent_RedeemFeeQuoteFields(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 4000)

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pk2, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		// 10% fee: 1000 -> fee 100, net redeemable 900.
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk1, AmountMloki: 1000, RedeemFeePpm: 100_000},
		// No fee configured (0 = free): full 3000 is redeemable.
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk2, AmountMloki: 3000},
	}))

	nip47Request := &models.Request{Method: constants.NIP47MethodListRecipients}
	var response *models.Response
	NewTestNip47Controller(svc).HandleListRecipientsEvent(context.TODO(), nip47Request, 1, wallet, func(r *models.Response, _ nostr.Tags) {
		response = r
	})
	require.Nil(t, response.Error)

	result, ok := response.Result.(listRecipientsResponse)
	require.True(t, ok)
	require.Len(t, result.Recipients, 2)

	byIdentity := map[string]recipientStatus{}
	for _, r := range result.Recipients {
		byIdentity[r.IdentityValue] = r
	}

	r1, ok := byIdentity[pk1]
	require.True(t, ok)
	assert.Equal(t, int64(100), r1.RedeemFeeMloki)
	assert.Equal(t, int64(900), r1.NetRedeemableMloki)

	r2, ok := byIdentity[pk2]
	require.True(t, ok)
	assert.Zero(t, r2.RedeemFeeMloki)
	assert.Equal(t, int64(3000), r2.NetRedeemableMloki)
}

// TestHandleListRecipientsEvent_RedeemFeeQuote_IsWorstCaseCeiling proves the
// documented "ceiling, never a floor" property: the quote reflects the
// EXTERNAL-case fee even though this exact wallet/claim, once actually
// redeemed, might resolve to a fee-free same-node payment instead — list_
// recipients has no invoice to check that in advance (NIP-CASH.md §Listing
// Recipients), so it always quotes the worst case.
func TestHandleListRecipientsEvent_RedeemFeeQuote_IsWorstCaseCeiling(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)

	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 1000, RedeemFeePpm: 250_000}, // 25%
	}))

	nip47Request := &models.Request{Method: constants.NIP47MethodListRecipients}
	var response *models.Response
	NewTestNip47Controller(svc).HandleListRecipientsEvent(context.TODO(), nip47Request, 1, wallet, func(r *models.Response, _ nostr.Tags) {
		response = r
	})
	require.Nil(t, response.Error)

	result := response.Result.(listRecipientsResponse)
	require.Len(t, result.Recipients, 1)
	assert.Equal(t, int64(250), result.Recipients[0].RedeemFeeMloki)
	assert.Equal(t, int64(750), result.Recipients[0].NetRedeemableMloki)
}
