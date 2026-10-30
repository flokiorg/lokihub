//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipCO"
	coclient "github.com/ohstr/nmilat/nipCO/client"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// NIP-CO §Order State and §Client Behavior: past pay_by a client reads an open
// order with no bill as ended unpaid, whatever its lookup answers. An on-chain
// order paid before pay_by whose chain scan fails at pay_by is refused for a
// retry (INTERNAL), never answered open; once the chain reads again it is
// closed and minted. An unpaid order past pay_by on a healthy chain is expired.
func TestCashOrder_LookupPastPayByWhileChainUnreadable(t *testing.T) {
	cfg := requireConfig(t)
	hooks := requireStackHooks(t, cfg, "GET /chain/tip", "POST /faults")
	oc, _, _ := openOrderShop(t, "order-window")

	paidJ, unpaidJ := newMemJournal(), newMemJournal()
	paid, err := oc.Place(ctxT(t), nipCO.CashOrderParams{Recipient: nipcash.Pubkey(mustPubkey(t, newTestPrivkey(t))),
		AmountMillis: 999_000, Payment: nipCO.PaymentOnchain}, paidJ, testDevice)
	require.NoError(t, err)
	_, err = oc.Place(ctxT(t), nipCO.CashOrderParams{Recipient: nipcash.Pubkey(mustPubkey(t, newTestPrivkey(t))),
		AmountMillis: 999_000, Payment: nipCO.PaymentOnchain}, unpaidJ, testDevice)
	require.NoError(t, err)

	// Paid in time, in the mempool; then the wallet cannot be read, and pay_by passes.
	hooks.setFaults(t, map[string]bool{"fail_chain_scan": true})
	_, err = hooks.PayAddress(paid.Address, paid.PayMillis)
	require.NoError(t, err)
	hooks.hubClockOffset(t, paid.PayBy-time.Now().Unix()+120)

	lo, order, err := oc.Lookup(ctxT(t), paidJ.calls[0])
	require.NoError(t, err)
	require.Nil(t, order, "no answer past pay_by while the window cannot be ended")
	assert.Equal(t, constants.ERROR_INTERNAL, lo.Code)
	_, unpaidOrder, err := oc.Lookup(ctxT(t), unpaidJ.calls[0])
	require.NoError(t, err)
	require.Nil(t, unpaidOrder, "nor for an unpaid one: the Hub cannot know it is unpaid")

	// The chain reads again: the paid order is closed and minted, the unpaid one expired.
	hooks.setFaults(t, map[string]bool{"fail_chain_scan": false})
	require.NoError(t, hooks.Mine(ctxT(t), 1))
	var got *nipCO.Order
	require.Eventually(t, func() bool {
		_, o, err := oc.Lookup(ctxT(t), paidJ.calls[0])
		if err != nil || o == nil {
			return false
		}
		got = o
		return len(o.Bills) == 1
	}, 3*time.Minute, 3*time.Second, "the payment seen before pay_by is minted")
	assert.Equal(t, nipCO.OrderClosed, got.State)
	assert.False(t, coclient.EndedUnpaid(got, false, time.Now().Unix()+3600))

	_, expired, err := oc.Lookup(ctxT(t), unpaidJ.calls[0])
	require.NoError(t, err)
	require.NotNil(t, expired)
	assert.Equal(t, nipCO.OrderExpired, expired.State)
	assert.True(t, coclient.EndedUnpaid(expired, false, expired.PayBy+1))
}
