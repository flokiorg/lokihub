//go:build integration

package integration

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// NIP-CASH §Lifecycle and Deletion (CASH-447, CASH-449): an owner delete takes
// the same first-wins claim as cash_redeem and never deletes a wallet while a
// redeem payment from it is in flight. Raced against a live redeem, exactly one
// takes the slice: either the holder is paid and the Hub gets only what is
// left, or the wallet is gone with the whole amount back at the Hub and the
// redeem refused. Value is conserved either way.
func TestOwnerDeleteRacingRedeem_OneWinnerAndValueConserved(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubAppID, admin := createEphemeralCashHub(t, cfg, "delete-redeem-race-cash-hub", nil)
	hubClient := mustConnect(t, hub.Connection)

	for trial := 0; trial < 4; trial++ {
		priv := newTestPrivkey(t)
		pub := mustPubkey(t, priv)
		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(pub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		walletAppID, ok := cashWalletOf(t, admin, hubAppID, pub)
		require.True(t, ok, "trial %d: the minted wallet is listed under its Hub", trial)
		bill := mustConnectBill(t, created.CashToken, priv)
		hubBefore := balanceOf(t, hubClient)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration delete-redeem race")
		proof := buildClaimProofEvent(t, priv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now())
		var redeemErr, deleteErr error
		var redeemed ClaimFundsResult
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			redeemErr = bill.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
				Invoice: invoice.Invoice, IdentityType: "pubkey", IdentityValue: pub, IdentityEvent: eventJSON(t, proof),
			}, &redeemed)
		}()
		go func() { defer wg.Done(); <-start; deleteErr = admin.deleteCashWallet(hubAppID, walletAppID) }()
		close(start)
		wg.Wait()

		paid := redeemErr == nil && redeemed.Preimage != ""
		if deleteErr != nil {
			require.True(t, paid, "trial %d: a refused delete means the redeem won: redeem %v, delete %v", trial, redeemErr, deleteErr)
			// The payout settled: the owner deletes what is left, unless the
			// drained bill already deleted itself.
			require.Eventually(t, func() bool {
				if _, ok := cashWalletOf(t, admin, hubAppID, pub); !ok {
					return true
				}
				return admin.deleteCashWallet(hubAppID, walletAppID) == nil
			}, time.Minute, 2*time.Second, "trial %d", trial)
		}
		gain := balanceOf(t, hubClient) - hubBefore
		if paid {
			require.Less(t, gain, int64(happyPathAmountMloki), "trial %d: the Hub never also takes a paid slice's value", trial)
		} else {
			require.Error(t, redeemErr, "trial %d", trial)
			require.EqualValues(t, happyPathAmountMloki, gain, "trial %d: the whole amount went back to the Hub", trial)
		}
	}
}

// cashWalletOf answers the app id of the wallet holding recipient's slice
// under hubAppID. Cash wallets are left out of the general app list, so they
// are found through their Hub's claims.
func cashWalletOf(t *testing.T, admin *adminClient, hubAppID uint, recipient string) (uint, bool) {
	t.Helper()
	claims, err := admin.listCashWalletClaims(hubAppID)
	require.NoError(t, err)
	for _, c := range claims {
		if c.IdentityValue == recipient {
			return c.WalletAppID, true
		}
	}
	return 0, false
}
