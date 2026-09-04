//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ohstr/nmilat/nipcash"
	cashclient "github.com/ohstr/nmilat/nipcash/client"
	"github.com/ohstr/nmilat/nipcw"
	cwclient "github.com/ohstr/nmilat/nipcw/client"
)

// This file drives lokihub's cash_hub/circle_hub implementations with
// nmilat's own published nipcash/nipcw client SDK (github.com/ohstr/nmilat),
// rather than this suite's hand-rolled wire.go structs + generic
// integration/nwcclient.Client - proof that lokihub-as-Hub is wire-compatible
// with the real caller-side library described in
// docs/nips/nmilat-migration-examples.md, not just internally self-consistent.
//
// The rest of this suite (cash_hub_test.go, cash_redeem_test.go, the
// *_audit_*/adversarial files, ...) deliberately keeps using the raw
// nwcclient path: those tests exist to send malformed/adversarial wire
// payloads (invalid identity_type, tampered proofs, wrong-amount bindings,
// ...) that nipcash's typed API refuses to construct client-side in the
// first place (see e.g. ErrMixedBearerAllocation) - the point of those
// tests is exercising the Hub's own server-side rejection, which a
// well-behaved SDK client can't be coerced into attempting.

const nmilatHappyPathAmountMillis = 5_000
const nmilatHappyPathExpiry = time.Hour

func TestNmilatSDK_CashHub_MintRedeemTransferConsolidate(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "nmilat-cash-hub", nil)

	hubClient, err := cashclient.Connect(ctxT(t), hub.Connection)
	require.NoError(t, err)
	t.Cleanup(hubClient.Close)

	t.Run("MintRedeem_Pubkey", func(t *testing.T) {
		priv := newTestPrivkey(t)
		pub := mustPubkey(t, priv)

		result, err := hubClient.MintCash(ctxT(t), nipcash.MintCashParams{
			Recipients: []nipcash.Allocation{nipcash.Send(nipcash.Pubkey(pub), nmilatHappyPathAmountMillis)},
			Expiry:     nmilatHappyPathExpiry,
		})
		require.NoError(t, err)
		require.NotEmpty(t, result.PairingURI)

		wallet, err := cashclient.Connect(ctxT(t), result.PairingURI)
		require.NoError(t, err)
		t.Cleanup(wallet.Close)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, nmilatHappyPathAmountMillis, "nmilat sdk redeem")
		redeemResult, err := wallet.CashRedeem(ctxT(t), nipcash.CashRedeemParams{
			Invoice:    invoice.Invoice,
			Credential: nipcash.BySigning(priv),
		})
		require.NoError(t, err)
		require.NotEmpty(t, redeemResult.Preimage)
	})

	t.Run("MintRedeem_Bearer", func(t *testing.T) {
		result, err := hubClient.MintCash(ctxT(t), nipcash.MintCashParams{
			Recipients: []nipcash.Allocation{nipcash.Send(nipcash.Anyone(), nmilatHappyPathAmountMillis)},
			Expiry:     nmilatHappyPathExpiry,
		})
		require.NoError(t, err)
		require.Len(t, result.Recipients, 1)
		secret := result.Recipients[0].BearerSecret
		require.NotEmpty(t, secret, "the plaintext bearer secret must come back exactly once, here")

		wallet, err := cashclient.Connect(ctxT(t), result.PairingURI)
		require.NoError(t, err)
		t.Cleanup(wallet.Close)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, nmilatHappyPathAmountMillis, "nmilat sdk bearer redeem")
		redeemResult, err := wallet.CashRedeem(ctxT(t), nipcash.CashRedeemParams{
			Invoice:    invoice.Invoice,
			Credential: nipcash.BySecret(secret),
		})
		require.NoError(t, err)
		require.NotEmpty(t, redeemResult.Preimage)
	})

	t.Run("Transfer_FullInPlace", func(t *testing.T) {
		srcPriv := newTestPrivkey(t)
		srcPub := mustPubkey(t, srcPriv)
		dstPriv := newTestPrivkey(t)
		dstPub := mustPubkey(t, dstPriv)

		minted, err := hubClient.MintCash(ctxT(t), nipcash.MintCashParams{
			Recipients: []nipcash.Allocation{nipcash.Send(nipcash.Pubkey(srcPub), nmilatHappyPathAmountMillis)},
			Expiry:     nmilatHappyPathExpiry,
		})
		require.NoError(t, err)

		wallet, err := cashclient.Connect(ctxT(t), minted.PairingURI)
		require.NoError(t, err)
		t.Cleanup(wallet.Close)

		transferResult, err := wallet.CashTransfer(ctxT(t), nipcash.CashTransferParams{
			Credential:    nipcash.BySigning(srcPriv),
			To:            nipcash.Pubkey(dstPub),
			CurrentAmount: nmilatHappyPathAmountMillis,
		})
		require.NoError(t, err)
		require.Empty(t, transferResult.NewWalletToken, "an in-place full transfer reuses the same connection/token")

		invoice := mintInvoiceFromSimpleWallet(t, cfg, nmilatHappyPathAmountMillis, "nmilat sdk transfer redeem")
		redeemResult, err := wallet.CashRedeem(ctxT(t), nipcash.CashRedeemParams{
			Invoice:    invoice.Invoice,
			Credential: nipcash.BySigning(dstPriv),
		})
		require.NoError(t, err)
		require.NotEmpty(t, redeemResult.Preimage)
	})

	t.Run("Transfer_Split", func(t *testing.T) {
		srcPriv := newTestPrivkey(t)
		srcPub := mustPubkey(t, srcPriv)
		dstPriv := newTestPrivkey(t)
		dstPub := mustPubkey(t, dstPriv)

		const total = uint64(nmilatHappyPathAmountMillis * 2)
		const splitAmount = uint64(nmilatHappyPathAmountMillis)

		minted, err := hubClient.MintCash(ctxT(t), nipcash.MintCashParams{
			Recipients: []nipcash.Allocation{nipcash.Send(nipcash.Pubkey(srcPub), total)},
			Expiry:     nmilatHappyPathExpiry,
		})
		require.NoError(t, err)

		wallet, err := cashclient.Connect(ctxT(t), minted.PairingURI)
		require.NoError(t, err)
		t.Cleanup(wallet.Close)

		split := splitAmount
		transferResult, err := wallet.CashTransfer(ctxT(t), nipcash.CashTransferParams{
			Credential:    nipcash.BySigning(srcPriv),
			To:            nipcash.Pubkey(dstPub),
			CurrentAmount: total,
			SplitAmount:   &split,
		})
		require.NoError(t, err)
		require.NotEmpty(t, transferResult.NewWalletToken, "split-off piece for the recipient")
		require.NotEmpty(t, transferResult.RemainderWalletToken, "caller's own change, in a brand-new wallet")

		newWallet, err := cashclient.Connect(ctxT(t), transferResult.NewWalletToken)
		require.NoError(t, err)
		t.Cleanup(newWallet.Close)
		recipientInvoice := mintInvoiceFromSimpleWallet(t, cfg, splitAmount, "nmilat sdk split redeem (recipient)")
		recipientRedeem, err := newWallet.CashRedeem(ctxT(t), nipcash.CashRedeemParams{
			Invoice:    recipientInvoice.Invoice,
			Credential: nipcash.BySigning(dstPriv),
		})
		require.NoError(t, err)
		require.NotEmpty(t, recipientRedeem.Preimage)

		remainderWallet, err := cashclient.Connect(ctxT(t), transferResult.RemainderWalletToken)
		require.NoError(t, err)
		t.Cleanup(remainderWallet.Close)
		remainderInvoice := mintInvoiceFromSimpleWallet(t, cfg, total-splitAmount, "nmilat sdk split redeem (remainder)")
		remainderRedeem, err := remainderWallet.CashRedeem(ctxT(t), nipcash.CashRedeemParams{
			Invoice:    remainderInvoice.Invoice,
			Credential: nipcash.BySigning(srcPriv),
		})
		require.NoError(t, err)
		require.NotEmpty(t, remainderRedeem.Preimage)
	})

	t.Run("Consolidate", func(t *testing.T) {
		ownerPriv := newTestPrivkey(t)
		ownerPub := mustPubkey(t, ownerPriv)

		mintedA, err := hubClient.MintCash(ctxT(t), nipcash.MintCashParams{
			Recipients: []nipcash.Allocation{nipcash.Send(nipcash.Pubkey(ownerPub), nmilatHappyPathAmountMillis)},
			Expiry:     nmilatHappyPathExpiry,
		})
		require.NoError(t, err)
		mintedB, err := hubClient.MintCash(ctxT(t), nipcash.MintCashParams{
			Recipients: []nipcash.Allocation{nipcash.Send(nipcash.Pubkey(ownerPub), nmilatHappyPathAmountMillis)},
			Expiry:     nmilatHappyPathExpiry,
		})
		require.NoError(t, err)

		walletA, err := cashclient.Connect(ctxT(t), mintedA.PairingURI)
		require.NoError(t, err)
		t.Cleanup(walletA.Close)

		consolidateResult, err := walletA.CashConsolidate(ctxT(t), nipcash.CashConsolidateParams{
			Sources: []nipcash.Source{
				nipcash.From(mintedA.WalletPubkey, nmilatHappyPathAmountMillis, nipcash.BySigning(ownerPriv)),
				nipcash.From(mintedB.WalletPubkey, nmilatHappyPathAmountMillis, nipcash.BySigning(ownerPriv)),
			},
			To: nipcash.Pubkey(ownerPub),
		})
		require.NoError(t, err)
		require.NotEmpty(t, consolidateResult.NewWalletToken)
		require.EqualValues(t, nmilatHappyPathAmountMillis*2, consolidateResult.AmountMillis)

		merged, err := cashclient.Connect(ctxT(t), consolidateResult.NewWalletToken)
		require.NoError(t, err)
		t.Cleanup(merged.Close)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, nmilatHappyPathAmountMillis*2, "nmilat sdk consolidate redeem")
		redeemResult, err := merged.CashRedeem(ctxT(t), nipcash.CashRedeemParams{
			Invoice:    invoice.Invoice,
			Credential: nipcash.BySigning(ownerPriv),
		})
		require.NoError(t, err)
		require.NotEmpty(t, redeemResult.Preimage)
	})
}

// TestNmilatSDK_CircleWallet_CreateAndRedeemCashIntoIt exercises nipcw's
// client alongside nipcash's, composing the two the way
// docs/nips/nmilat-migration-examples.md's "Redeeming Straight Into a
// Circle Wallet" section describes: a member self-serves a Circle Wallet
// via nipcw, then redeems a cash token minted by a same-host Cash Hub
// straight into it - the one invoice-amount case that always resolves
// same-node, full face value, zero fee.
func TestNmilatSDK_CircleWallet_CreateAndRedeemCashIntoIt(t *testing.T) {
	cfg := requireConfig(t)

	memberPriv := newTestPrivkey(t)
	memberPub := mustPubkey(t, memberPriv)

	circleHub, _, _ := createEphemeralCircleHub(t, cfg, "nmilat-circle-hub", circlePolicyAllowlist, []string{memberPriv}, ephemeralCircleHubOpts{})

	circleHubClient, err := cwclient.Connect(ctxT(t), circleHub.Connection)
	require.NoError(t, err)
	t.Cleanup(circleHubClient.Close)

	resp, err := circleHubClient.CreateCircleWallet(ctxT(t), nipcw.CreateCircleWalletParams{
		Credential:      nipcw.BySigning(memberPriv),
		MaxAmountMillis: nmilatHappyPathAmountMillis,
		Expiry:          nmilatHappyPathExpiry,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.PairingURI, "resp.PairingURI is already decrypted - connect straight from it")

	// The Circle Wallet itself is an ordinary NWC connection once created -
	// generic calls against it (make_invoice/get_balance) go through this
	// suite's existing nwcclient, same as the rest of the suite; only the
	// NIP-CASH/NIP-CW protocol calls above go through nmilat's typed SDK.
	member := mustConnect(t, resp.PairingURI)

	cashHub, _, _ := createEphemeralCashHub(t, cfg, "nmilat-cash-hub-for-circle", nil)
	cashHubClient, err := cashclient.Connect(ctxT(t), cashHub.Connection)
	require.NoError(t, err)
	t.Cleanup(cashHubClient.Close)

	minted, err := cashHubClient.MintCash(ctxT(t), nipcash.MintCashParams{
		Recipients: []nipcash.Allocation{nipcash.Send(nipcash.Pubkey(memberPub), nmilatHappyPathAmountMillis)},
		Expiry:     nmilatHappyPathExpiry,
	})
	require.NoError(t, err)

	cashWallet, err := cashclient.Connect(ctxT(t), minted.PairingURI)
	require.NoError(t, err)
	t.Cleanup(cashWallet.Close)

	var invoice MakeInvoiceResult
	require.NoError(t, member.Call(ctxT(t), "make_invoice", MakeInvoiceParams{
		Amount:      nmilatHappyPathAmountMillis,
		Description: "nmilat sdk circle wallet redeem",
	}, &invoice))
	require.NotEmpty(t, invoice.Invoice)

	redeemResult, err := cashWallet.CashRedeem(ctxT(t), nipcash.CashRedeemParams{
		Invoice:    invoice.Invoice,
		Credential: nipcash.BySigning(memberPriv),
	})
	require.NoError(t, err)
	require.NotEmpty(t, redeemResult.Preimage)

	var balance GetBalanceResult
	require.NoError(t, member.Call(ctxT(t), "get_balance", struct{}{}, &balance))
	require.EqualValues(t, nmilatHappyPathAmountMillis, balance.Balance, "same-host redemption resolves same-node: full face value, zero fee")
}
