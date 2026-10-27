//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipCO"
	coclient "github.com/ohstr/nmilat/nipCO/client"
	"github.com/ohstr/nmilat/nipcash"
	cashclient "github.com/ohstr/nmilat/nipcash/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orderTerms are the selling terms these tests put on an ephemeral Cash Hub
// (per-wallet ceiling 10,000,000 millis).
func orderTerms() map[string]any {
	return map[string]any{
		"terms": nipCO.Terms{
			Lightning: &nipCO.KindTerms{MinMillis: 1000, MaxMillis: 1_000_000, FeeBase: 1000, FeePpm: 1000, Window: 600},
			Onchain: &nipCO.KindTerms{MinMillis: 10_000, MaxMillis: 1_000_000, FeeBase: 21000, FeePpm: 2000, Window: 3600,
				Confirmations: []nipCO.Tier{{N: 1}}},
			Bill: nipCO.BillTerms{RedeemFeePpm: 0, MinTransferMillis: 0},
		},
		"openCeilingLightningMillis": 5_000_000,
		"openCeilingOnchainMillis":   5_000_000,
		"openOnchainMax":             5,
	}
}

// openOrderShop sets order terms on a fresh Cash Hub and connects to a new Order
// connection of it with nmilat's order client.
func openOrderShop(t *testing.T, name string) (*coclient.OrderClient, uint, *adminClient) {
	t.Helper()
	cfg := requireConfig(t)
	_, hubID, admin := createEphemeralCashHub(t, cfg, name, nil)
	require.NoError(t, admin.doBody(http.MethodPut, fmt.Sprintf("/api/cash-hubs/%d/order-terms", hubID), orderTerms(), nil))
	var conn struct {
		ID               uint   `json:"id"`
		ConnectionString string `json:"connectionString"`
	}
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/cash-hubs/%d/order-connections", hubID),
		map[string]string{"label": "integration shop"}, &conn))
	require.NotEmpty(t, conn.ConnectionString)
	oc, err := coclient.Connect(ctxT(t), conn.ConnectionString, coclient.Options{})
	require.NoError(t, err)
	t.Cleanup(oc.Close)
	require.True(t, oc.Verified(), "the cashorder1 string is signed by the node")
	return oc, hubID, admin
}

// I-43, I-44: a Lightning order is placed on an Order connection, paid by a
// wallet on the same node, minted into one ordered bill the buyer reads with
// cash_lookup; the Hub's owner cannot delete it; its recipient redeems it.
func TestCashOrder_LightningOrderMintedAndRedeemed(t *testing.T) {
	cfg := requireConfig(t)
	oc, hubID, admin := openOrderShop(t, "order-lightning")
	terms, err := oc.Terms(ctxT(t))
	require.NoError(t, err)
	require.NotNil(t, terms.Lightning)

	recipientPriv := newTestPrivkey(t)
	j := newMemJournal()
	order, err := oc.Place(ctxT(t), nipCO.CashOrderParams{Recipient: nipcash.Pubkey(mustPubkey(t, recipientPriv)),
		AmountMillis: 21_000, Payment: nipCO.PaymentLightning}, j, testDevice)
	require.NoError(t, err)
	assert.Equal(t, nipCO.OrderOpen, order.State)
	assert.EqualValues(t, 22_021, order.PayMillis)
	require.NotEmpty(t, order.Invoice)
	require.Len(t, j.calls, 1)

	payInvoiceFromSimpleWallet(t, cfg, order.Invoice)

	var got *nipCO.Order
	require.Eventually(t, func() bool {
		_, o, err := oc.Lookup(ctxT(t), j.calls[0])
		if err != nil || o == nil {
			return false
		}
		got = o
		return coclient.Finished(o)
	}, 60*time.Second, time.Second, "the order is minted on its own")
	assert.Equal(t, nipCO.OrderMinted, got.State)
	require.Len(t, got.Bills, 1)
	assert.EqualValues(t, 21_000, got.Bills[0].AmountMillis)
	assert.EqualValues(t, 22_021, got.Bills[0].PaidMillis)

	billAppID, err := admin.findAppIDByWalletPubkey(got.Bills[0].WalletPubkey)
	require.NoError(t, err)
	err = admin.deleteCashWallet(hubID, billAppID)
	require.Error(t, err, "an ordered bill is not the owner's to delete")
	assert.Contains(t, err.Error(), "ordered")

	bills, err := coclient.OrderedBills(got)
	require.NoError(t, err)
	require.Len(t, bills, 1)
	wallet, err := cashclient.Connect(ctxT(t), got.Bills[0].Token.Token)
	require.NoError(t, err)
	t.Cleanup(wallet.Close)
	payee := mustConnect(t, createEphemeralSimpleWallet(t, cfg).Connection)
	var invoice MakeInvoiceResult
	require.NoError(t, payee.Call(ctxT(t), "make_invoice", MakeInvoiceParams{Amount: 21_000, Description: "integration ordered bill redeem"}, &invoice))
	_, err = wallet.CashRedeem(ctxT(t), nipcash.CashRedeemParams{Invoice: invoice.Invoice, Credential: nipcash.BySigning(recipientPriv)})
	require.NoError(t, err)
	assert.EqualValues(t, 21_000, balanceOf(t, payee))
}

// I-45: an on-chain order gets a fresh address of the Hub's network with its
// confirmations; a retired connection refuses new orders and reports {} terms.
func TestCashOrder_OnchainPlacedAndRetiredConnection(t *testing.T) {
	oc, hubID, admin := openOrderShop(t, "order-onchain")
	order, err := oc.Place(ctxT(t), nipCO.CashOrderParams{Recipient: nipcash.Pubkey(mustPubkey(t, newTestPrivkey(t))),
		AmountMillis: 21_000, Payment: nipCO.PaymentOnchain}, newMemJournal(), testDevice)
	require.NoError(t, err)
	assert.NotEmpty(t, order.Address)
	assert.EqualValues(t, 1, order.Confirmations)
	assert.Zero(t, order.PayMillis%1000)

	var conns []struct {
		ID uint `json:"id"`
	}
	require.NoError(t, admin.do(http.MethodGet, fmt.Sprintf("/api/cash-hubs/%d/order-connections", hubID), &conns))
	require.Len(t, conns, 1)
	require.NoError(t, admin.do(http.MethodPost, fmt.Sprintf("/api/cash-hubs/%d/order-connections/%d/retire", hubID, conns[0].ID), nil))
	terms, err := oc.Terms(ctxT(t))
	require.NoError(t, err)
	assert.True(t, terms.SellsNothing(), "a retired connection answers cash_order {}")
}
