//go:build integration

// cash_redeem_fee_test.go covers the Cash Hub redeem fee (NIP-CASH.md §The
// Redeem Fee) end to end, over real NWC/Nostr, against a real running
// instance: a Hub-configured redeem_fee_ppm is quoted upfront via
// list_recipients, and cash_redeem's own same-node exemption (transactions.
// IsSelfPayment) waives the fee entirely for a redemption that resolves to a
// payment this same node is both sending and receiving — every redemption
// this test suite's mintInvoiceFromSimpleWallet helper can produce, since it
// mints from a wallet hosted on this same instance.
package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

func TestCashRedeemFee_SameNodeExemptAndListRecipientsQuote(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	const redeemFeePpm = 100_000 // 10%

	req := adminCreateAppRequest{
		Name:                  ephemeralFixtureNamePrefix + " redeem-fee-hub",
		Scopes:                []string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.MAKE_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                  "cash_hub",
		CashPerWalletMaxMloki: 10_000_000,
		CashMaxExpSecs:        3600,
		CashRedeemFeePpm:      redeemFeePpm,
	}
	resp, err := admin.createApp(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := admin.deleteApp(resp.ID); err != nil {
			t.Logf("cleanup: failed to delete ephemeral hub app_id=%d (%v)", resp.ID, err)
		}
	})
	t.Cleanup(func() {
		claims, err := admin.listCashWalletClaims(resp.ID)
		if err != nil {
			return
		}
		seen := map[uint]bool{}
		for _, claim := range claims {
			if seen[claim.WalletAppID] {
				continue
			}
			seen[claim.WalletAppID] = true
			_ = admin.deleteCashWallet(resp.ID, claim.WalletAppID)
		}
	})
	require.NoError(t, admin.transfer(nil, resp.ID, ephemeralCashHubFundLoki))
	hubClient := mustConnect(t, resp.PairingUri)

	beneficiaryPriv := newTestPrivkey(t)
	beneficiaryPub := mustPubkey(t, beneficiaryPriv)

	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))
	shared := mustConnect(t, created.PairingURI)

	t.Run("ListRecipients_QuotesWorstCaseFee", func(t *testing.T) {
		var recipients ListRecipientsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodListRecipients, struct{}{}, &recipients))
		require.Len(t, recipients.Recipients, 1)
		r := recipients.Recipients[0]

		wantFee := int64(happyPathAmountMloki) * redeemFeePpm / 1_000_000
		require.EqualValues(t, wantFee, r.RedeemFeeMillis, "list_recipients must quote this slice's own redeem_fee_ppm cut")
		require.EqualValues(t, happyPathAmountMloki-wantFee, r.NetRedeemableMillis)
	})

	t.Run("SameNodeRedemption_FeeWaived_FullAmountPaid", func(t *testing.T) {
		// mintInvoiceFromSimpleWallet mints from a wallet on THIS same
		// instance - transactions.IsSelfPayment resolves this as same-node,
		// so the redeem fee must be waived entirely despite the nonzero
		// redeem_fee_ppm configured on this hub.
		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration redeem-fee same-node test")
		proof := buildClaimProofEvent(t, beneficiaryPriv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now())

		var result ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: beneficiaryPub,
			IdentityEvent: eventJSON(t, proof),
		}, &result))
		require.NotEmpty(t, result.Preimage)
		require.Zero(t, result.FeesPaid, "a same-node redemption must be fee-free even with a nonzero redeem_fee_ppm configured")
	})
}

// TestCashRedeemFee_QuotedNetAmount_RejectedForSameNodeRedemption proves the
// converse of the same-node exemption: presenting the fee-REDUCED net amount
// (the correct amount for a genuinely external redemption) against an
// invoice that actually resolves to a same-node payment must be rejected -
// a same-node redemption always requires the FULL slice, never the
// fee-reduced net.
func TestCashRedeemFee_QuotedNetAmount_RejectedForSameNodeRedemption(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}

	const redeemFeePpm = 100_000 // 10%

	req := adminCreateAppRequest{
		Name:                  ephemeralFixtureNamePrefix + " redeem-fee-mismatch-hub",
		Scopes:                []string{constants.CASH_HUB_SCOPE, constants.PAY_INVOICE_SCOPE, constants.MAKE_INVOICE_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                  "cash_hub",
		CashPerWalletMaxMloki: 10_000_000,
		CashMaxExpSecs:        3600,
		CashRedeemFeePpm:      redeemFeePpm,
	}
	resp, err := admin.createApp(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := admin.deleteApp(resp.ID); err != nil {
			t.Logf("cleanup: failed to delete ephemeral hub app_id=%d (%v)", resp.ID, err)
		}
	})
	t.Cleanup(func() {
		claims, err := admin.listCashWalletClaims(resp.ID)
		if err != nil {
			return
		}
		seen := map[uint]bool{}
		for _, claim := range claims {
			if seen[claim.WalletAppID] {
				continue
			}
			seen[claim.WalletAppID] = true
			_ = admin.deleteCashWallet(resp.ID, claim.WalletAppID)
		}
	})
	require.NoError(t, admin.transfer(nil, resp.ID, ephemeralCashHubFundLoki))
	hubClient := mustConnect(t, resp.PairingUri)

	beneficiaryPriv := newTestPrivkey(t)
	beneficiaryPub := mustPubkey(t, beneficiaryPriv)

	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &created))
	shared := mustConnect(t, created.PairingURI)

	netAmount := uint64(happyPathAmountMloki) - uint64(happyPathAmountMloki)*redeemFeePpm/1_000_000
	invoice := mintInvoiceFromSimpleWallet(t, cfg, netAmount, "integration redeem-fee mismatch test")
	proof := buildClaimProofEvent(t, beneficiaryPriv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now())

	var result ClaimFundsResult
	err = shared.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       invoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: beneficiaryPub,
		IdentityEvent: eventJSON(t, proof),
	}, &result)
	requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)

	// The slice must remain claimable with the correct (full) amount afterward.
	fullInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration redeem-fee mismatch retry")
	retryProof := buildClaimProofEvent(t, beneficiaryPriv, created.WalletPubkey, fullInvoice.PaymentHash, nil, time.Now())
	var retryResult ClaimFundsResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       fullInvoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: beneficiaryPub,
		IdentityEvent: eventJSON(t, retryProof),
	}, &retryResult))
	require.NotEmpty(t, retryResult.Preimage)
	require.Zero(t, retryResult.FeesPaid)
}
