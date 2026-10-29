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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// I-46 (paid half) and I-56 (unfinal): an on-chain order paid from the payer
// node is minted at its tier (1 confirmation); its bill is refused for
// cash_withdraw as unfinal until the payment has final_depth (6)
// confirmations, then withdrawn.
func TestCashOrder_OnchainPaidMintedAndUnfinal(t *testing.T) {
	cfg := requireConfig(t)
	hooks := requireStackHooks(t, cfg, "GET /chain/tip")
	h := newWithdrawHub(t, cfg, "order-onchain-paid")
	terms := map[string]any{
		"terms": nipCO.Terms{
			Onchain: &nipCO.KindTerms{MinMillis: 10_000, MaxMillis: 10_000_000, FeeBase: 21000, FeePpm: 2000, Window: 3600,
				Confirmations: []nipCO.Tier{{N: 1}}},
		},
		"openCeilingLightningMillis": 5_000_000,
		"openCeilingOnchainMillis":   50_000_000,
		"openOnchainMax":             5,
	}
	require.NoError(t, h.admin.doBody(http.MethodPut, fmt.Sprintf("/api/cash-hubs/%d/order-terms", h.id), terms, nil))
	var conn struct {
		ConnectionString string `json:"connectionString"`
	}
	require.NoError(t, h.admin.doBody(http.MethodPost, fmt.Sprintf("/api/cash-hubs/%d/order-connections", h.id),
		map[string]string{"label": "integration on-chain shop"}, &conn))
	oc, err := coclient.Connect(ctxT(t), conn.ConnectionString, coclient.Options{})
	require.NoError(t, err)
	t.Cleanup(oc.Close)

	recipient := newTestPrivkey(t)
	j := newMemJournal()
	order, err := oc.Place(ctxT(t), nipCO.CashOrderParams{Recipient: nipcash.Pubkey(mustPubkey(t, recipient)),
		AmountMillis: 5_000_000, Payment: nipCO.PaymentOnchain}, j, testDevice)
	require.NoError(t, err)
	require.NotEmpty(t, order.Address)
	_, err = hooks.PayAddress(order.Address, order.PayMillis)
	require.NoError(t, err)

	// One block: the payment reaches its 1-confirmation tier and is minted
	// (the intake scans every 30 s) while far from final_depth.
	require.NoError(t, hooks.Mine(ctxT(t), 1))
	var got *nipCO.Order
	require.Eventually(t, func() bool {
		_, o, err := oc.Lookup(ctxT(t), j.calls[len(j.calls)-1])
		if err == nil && o != nil {
			got = o
			return o.State == nipCO.OrderMinted
		}
		return false
	}, 3*time.Minute, 3*time.Second, "the payment is seen, confirmed at its tier and minted")
	require.Len(t, got.Bills, 1)
	assert.EqualValues(t, 5_000_000, got.Bills[0].AmountMillis)
	token := got.Bills[0].Token.Token

	q, err := quoteAs(t, token, recipient, map[string]any{"address": regtestP2WPKH(t)})
	require.NoError(t, err)
	_, err = withdrawAs(t, token, recipient, q, newTestPrivkey(t))
	requireDetail(t, err, constants.ERROR_RESTRICTED, nipcash.DetailUnfinal)

	require.NoError(t, hooks.Mine(ctxT(t), 6))
	require.Eventually(t, func() bool {
		q, err := quoteAs(t, token, recipient, map[string]any{"address": regtestP2WPKH(t)})
		if err != nil {
			return false
		}
		_, err = withdrawAs(t, token, recipient, q, newTestPrivkey(t))
		return err == nil
	}, 2*time.Minute, 5*time.Second, "withdrawn once the payment is at final_depth (the intake scan reads its depth every 30 s)")
	assert.NotEmpty(t, got.Bills[0].Outpoint)
}
