//go:build integration

package integration

import (
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// NIP-CW §Deleting a Circle Wallet: a same-node keysend to a member's wallet
// still to be credited refuses its delete_circle_wallet (RESTRICTED) and the
// wallet stays; the keysend then credits it. A sibling wallet with nothing in
// flight and no balance is deleted.
func TestDeleteCircleWallet_RefusedWhileASameNodeKeysendIsInFlight(t *testing.T) {
	cfg := requireConfig(t)
	hooks := requireStackHooks(t, cfg, "POST /faults")
	memberB, memberC := newTestPrivkey(t), newTestPrivkey(t)
	hub, _, admin := createEphemeralCircleHub(t, cfg, "delete-inflight", circlePolicyAllowlist,
		[]string{memberB, memberC}, ephemeralCircleHubOpts{FundLoki: 1_000})
	b := mintCircleChildFor(t, hub, memberB)
	c := mintCircleChildFor(t, hub, memberC)
	require.Zero(t, balanceOf(t, b))

	var info GetInfoResult
	require.NoError(t, b.Call(ctxT(t), "get_info", struct{}{}, &info))
	require.NotNil(t, info.Pubkey)
	bID, err := admin.findAppIDByWalletPubkey(b.WalletPubkey())
	require.NoError(t, err)
	namesB := []tlvRecord{{Type: customKeyTLVType, Value: hex.EncodeToString([]byte(strconv.FormatUint(uint64(bID), 10)))}}

	// Funded first: its funding transfer is itself a self-payment.
	outsider := mustConnect(t, createEphemeralSimpleWallet(t, cfg).Connection)
	hooks.setFaults(t, map[string]bool{"hold_self_payments": true})
	sent := make(chan error, 1)
	go func() {
		sent <- outsider.Call(ctxT(t), "pay_keysend", payKeysendParams{Amount: 1_000, Pubkey: *info.Pubkey, TLVRecords: namesB}, &PayInvoiceResult{})
	}()
	// The keysend's credit row is written before its settle is held.
	time.Sleep(2 * time.Second)

	var deleted map[string]interface{}
	err = b.Call(ctxT(t), constants.NIP47MethodDeleteCircleWallet, struct{}{}, &deleted)
	requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
	_, err = admin.findAppIDByWalletPubkey(b.WalletPubkey())
	require.NoError(t, err, "the wallet stays")

	hooks.setFaults(t, map[string]bool{"hold_self_payments": false})
	require.NoError(t, <-sent)
	assert.EqualValues(t, 1_000, balanceOf(t, b), "credited to the wallet that stayed")

	require.NoError(t, c.Call(ctxT(t), constants.NIP47MethodDeleteCircleWallet, struct{}{}, &deleted),
		"a wallet with nothing in flight and no balance is deleted")
}
