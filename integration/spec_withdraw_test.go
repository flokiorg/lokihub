//go:build integration

// spec_withdraw_test.go covers on-chain withdrawals at the Hub's surface (plan
// §5.1 I-53, I-54, I-56, I-66 and §5.2 I-63): terms and the switches,
// cash_withdraw_quote's refusal table, the call's step-3 refusals that need no
// chain, a committed payout's lookup and cancel request, and the switch turned
// off with a payout committed.
//
// NOT YET RUN LIVE: written with WP-23 on spec-sync/wp-23-withdraw and only
// compiled (go vet -tags integration). Each test skips unless the backend
// reports the on-chain capability (GET /api/cash-hubs/:id/withdraw-terms,
// "capable"), which flnd gains with WP-24's lnclient.OnchainPayer. The chain
// steps (mining past holds_until for quote_expired, the bound and starve hooks,
// an order address, payouts reaching final) need the regtest chain hooks of
// spec-sync/regtest-hooks and are in WP-24's spec_withdraw_payout_test.go.
package integration

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/flokiorg/go-flokicoin/chaincfg"
	"github.com/flokiorg/go-flokicoin/chainutil"
	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// withdrawTermsView is GET /api/cash-hubs/:id/withdraw-terms.
type withdrawTermsView struct {
	Enabled       bool     `json:"enabled"`
	FeeBaseMillis int64    `json:"fee_base_millis"`
	FeePpm        int64    `json:"fee_ppm"`
	MinMillis     *int64   `json:"min_millis"`
	MaxMillis     *int64   `json:"max_millis"`
	ScriptTypes   [][2]int `json:"script_types"`
	FinalDepth    int      `json:"final_depth"`
	NodeEnabled   bool     `json:"node_enabled"`
	Capable       bool     `json:"capable"`
	Offered       bool     `json:"offered"`
}

// withdrawHub is an ephemeral Cash Hub with withdrawals on, node and hub.
type withdrawHub struct {
	hub   CashHubConfig
	id    uint
	admin *adminClient
}

func setWithdrawNode(t *testing.T, admin *adminClient, on bool) {
	t.Helper()
	require.NoError(t, admin.doBody(http.MethodPut, "/api/cash-withdraw/node", map[string]bool{"enabled": on}, nil))
}

func newWithdrawHub(t *testing.T, cfg *Config, name string) withdrawHub {
	t.Helper()
	hub, id, admin := createEphemeralCashHub(t, cfg, name, nil)
	var v withdrawTermsView
	require.NoError(t, admin.do(http.MethodGet, fmt.Sprintf("/api/cash-hubs/%d/withdraw-terms", id), &v))
	if !v.Capable {
		t.Skip("skipping: the backend reports no on-chain capability (lnclient.OnchainPayer, WP-24)")
	}
	setWithdrawNode(t, admin, true)
	min := int64(1_000_000)
	require.NoError(t, admin.doBody(http.MethodPut, fmt.Sprintf("/api/cash-hubs/%d/withdraw-terms", id),
		map[string]any{"enabled": true, "fee_base_millis": 500_000, "fee_ppm": 2_000, "min_millis": min}, &v))
	require.True(t, v.Offered, "offered once the engine has reconciled")
	return withdrawHub{hub: hub, id: id, admin: admin}
}

// mintFor mints one pubkey slice per amount and returns the token and keys.
func (h withdrawHub) mintFor(t *testing.T, amounts ...uint64) (string, []string, []string) {
	t.Helper()
	privs, pubs := make([]string, len(amounts)), make([]string, len(amounts))
	params := make([]CashWalletRecipientParam, len(amounts))
	for i, a := range amounts {
		privs[i] = newTestPrivkey(t)
		pubs[i] = mustPubkey(t, privs[i])
		params[i] = CashWalletRecipientParam{IdentityType: "pubkey", IdentityValue: pubs[i], AmountMillis: a}
	}
	var created MintCashResult
	require.NoError(t, mustConnect(t, h.hub.Connection).Call(ctxT(t), constants.NIP47MethodMintCash,
		MintCashParams{Recipients: params, Expiry: happyPathExpirySecs}, &created))
	return created.CashToken, privs, pubs
}

// regtestP2WPKH is a fresh regtest key-hash address nobody spends from.
func regtestP2WPKH(t *testing.T) string {
	t.Helper()
	b, err := hex.DecodeString(nostr.GeneratePrivateKey()[:40])
	require.NoError(t, err)
	a, err := chainutil.NewAddressWitnessPubKeyHash(b, &chaincfg.RegressionNetParams)
	require.NoError(t, err)
	return a.EncodeAddress()
}

func requireDetail(t *testing.T, err error, code string, detail nipcash.Detail) {
	t.Helper()
	requireNWCErrorCode(t, err, code)
	var e *nwcclient.NWCError
	require.ErrorAs(t, err, &e)
	assert.Equal(t, string(detail), e.Detail)
}

// quoteAs asks a quote for the slice of priv.
func quoteAs(t *testing.T, token, priv string, params map[string]any) (*nipcash.WithdrawQuote, error) {
	t.Helper()
	var res nipcash.CashWithdrawQuoteResult
	if err := privateCall(t, token, constants.NIP47MethodCashWithdrawQuote, params, priv, &res); err != nil {
		return nil, err
	}
	q, err := nipcash.ParseWithdrawQuote(res.Quote)
	require.NoError(t, err)
	return q, nil
}

func withdrawAs(t *testing.T, token, priv string, q *nipcash.WithdrawQuote, rid string) (*nipcash.Payout, error) {
	t.Helper()
	tok, err := nipcash.Decode(token)
	require.NoError(t, err)
	req, err := nipcash.CashWithdrawParams{Quote: q, Credential: nipcash.BySigning(priv), RequestID: rid,
		Notify: newTestPrivkey(t)}.Request(tok.WalletPubkey)
	require.NoError(t, err)
	var out nipcash.Payout
	if err := privateCall(t, token, constants.NIP47MethodCashWithdraw, req, priv, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// I-53: the withdraw object and both methods follow the switches.
func TestSpecWithdraw_I53_Switches(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-switches")
	token, privs, _ := h.mintFor(t, 21_000_000)
	conn := mustConnectBill(t, token, privs[0])

	var st struct {
		Withdraw *nipcash.WithdrawTerms `json:"withdraw"`
	}
	require.NoError(t, conn.Call(ctxT(t), constants.NIP47MethodCashStatus, CashStatusParams{}, &st))
	require.NotNil(t, st.Withdraw)
	assert.Equal(t, [][2]int{{0, 20}, {0, 32}, {1, 32}}, st.Withdraw.ScriptTypes)
	assert.GreaterOrEqual(t, st.Withdraw.FinalDepth, 6)
	assert.Nil(t, st.Withdraw.MaxMillis)

	setWithdrawNode(t, h.admin, false)
	t.Cleanup(func() { setWithdrawNode(t, h.admin, true) })
	st.Withdraw = nil
	require.NoError(t, conn.Call(ctxT(t), constants.NIP47MethodCashStatus, CashStatusParams{}, &st))
	assert.Nil(t, st.Withdraw)
	_, err := quoteAs(t, token, privs[0], map[string]any{"address": regtestP2WPKH(t)})
	requireNWCErrorCode(t, err, constants.ERROR_NOT_IMPLEMENTED)
}

// I-54: the quote's refusal table, each row with its detail, and a quote that
// verifies with the SDK and changes nothing.
func TestSpecWithdraw_I54_Quote(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-quote")
	token, privs, pubs := h.mintFor(t, 21_000_000, 500_000)
	addr := regtestP2WPKH(t)
	testnet, err := chainutil.NewAddressWitnessPubKeyHash(make([]byte, 20), &chaincfg.TestNet3Params)
	require.NoError(t, err)
	p2pkh, err := chainutil.NewAddressPubKeyHash(make([]byte, 20), &chaincfg.RegressionNetParams)
	require.NoError(t, err)

	_, err = quoteAs(t, token, newTestPrivkey(t), map[string]any{"address": addr})
	requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)
	_, err = quoteAs(t, token, privs[0], map[string]any{"address": strings.ToUpper(addr)})
	requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	_, err = quoteAs(t, token, privs[0], map[string]any{"address": testnet.EncodeAddress()})
	requireDetail(t, err, constants.ERROR_RESTRICTED, nipcash.DetailNetwork)
	_, err = quoteAs(t, token, privs[0], map[string]any{"address": p2pkh.EncodeAddress()})
	requireDetail(t, err, constants.ERROR_RESTRICTED, nipcash.DetailScriptType)
	_, err = quoteAs(t, token, privs[1], map[string]any{"address": addr})
	requireDetail(t, err, constants.ERROR_RESTRICTED, nipcash.DetailBelowMin)
	_, err = quoteAs(t, token, privs[0], map[string]any{"address": addr, "send_before": time.Now().Unix() + 60})
	requireDetail(t, err, constants.ERROR_RESTRICTED, nipcash.DetailTooSlow)

	sendBefore := time.Now().Unix() + 1800
	var res nipcash.CashWithdrawQuoteResult
	require.NoError(t, privateCall(t, token, constants.NIP47MethodCashWithdrawQuote,
		map[string]any{"address": addr, "send_before": sendBefore}, privs[0], &res))
	tok, err := nipcash.Decode(token)
	require.NoError(t, err)
	minter, ok := nipcash.VerifyProvenance(tok)
	require.True(t, ok)
	q, err := nipcash.VerifyQuoteAnswer(res, nipcash.QuoteExpectation{HubXOnly: minter, Wallet: tok.WalletPubkey,
		IdentityValue: pubs[0], Address: addr, SendBefore: &sendBefore, AmountMillis: 21_000_000})
	require.NoError(t, err)
	assert.LessOrEqual(t, q.SentBy, sendBefore)
}

// I-63 and part of I-56: a committed call's reply is recovered by cash_lookup
// with the same receipt, a resend is REQUEST_REUSED, a quote for another slice
// is quote_invalid and an address of the Hub's own wallet own_address, each
// leaving the slice claimable; a cancel request before 7 days is RESTRICTED.
func TestSpecWithdraw_I63_LookupAndRefusals(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-call")
	token, privs, _ := h.mintFor(t, 21_000_000, 7_000_000)

	other, err := quoteAs(t, token, privs[1], map[string]any{"address": regtestP2WPKH(t)})
	require.NoError(t, err)
	_, err = withdrawAs(t, token, privs[0], other, newTestPrivkey(t))
	requireDetail(t, err, constants.ERROR_RESTRICTED, nipcash.DetailQuoteInvalid)

	q, err := quoteAs(t, token, privs[0], map[string]any{"address": regtestP2WPKH(t)})
	require.NoError(t, err)
	rid := newTestPrivkey(t)
	p, err := withdrawAs(t, token, privs[0], q, rid)
	require.NoError(t, err)
	assert.Equal(t, nipcash.PayoutQueued, p.State)

	var lk nipcash.CashLookupResult
	require.NoError(t, privateCall(t, token, constants.NIP47MethodCashLookup,
		nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Add(time.Minute).Unix()}, privs[0], &lk))
	assert.Equal(t, nipcash.LookupDone, lk.Status)
	lp, err := nipcash.ParsePayout(lk.Result)
	require.NoError(t, err)
	assert.Equal(t, p.Receipt, lp.Receipt)

	_, err = withdrawAs(t, token, privs[0], q, rid)
	requireNWCErrorCode(t, err, constants.ERROR_REQUEST_REUSED)

	err = privateCall(t, token, constants.NIP47MethodCashWithdraw, map[string]any{"cancel": true, "request_id": rid}, privs[0], nil)
	requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
}

// I-66 (the part without a chain): with the switch off, new quotes and calls
// are NOT_IMPLEMENTED while a committed payout is still looked up and its
// cancel request answered.
func TestSpecWithdraw_I66_SwitchOffKeepsPayouts(t *testing.T) {
	cfg := requireConfig(t)
	h := newWithdrawHub(t, cfg, "withdraw-off")
	token, privs, _ := h.mintFor(t, 21_000_000, 7_000_000)
	q, err := quoteAs(t, token, privs[0], map[string]any{"address": regtestP2WPKH(t)})
	require.NoError(t, err)
	rid := newTestPrivkey(t)
	_, err = withdrawAs(t, token, privs[0], q, rid)
	require.NoError(t, err)

	setWithdrawNode(t, h.admin, false)
	t.Cleanup(func() { setWithdrawNode(t, h.admin, true) })
	_, err = quoteAs(t, token, privs[1], map[string]any{"address": regtestP2WPKH(t)})
	requireNWCErrorCode(t, err, constants.ERROR_NOT_IMPLEMENTED)
	var lk nipcash.CashLookupResult
	require.NoError(t, privateCall(t, token, constants.NIP47MethodCashLookup,
		nipcash.CashLookupRequest{RequestID: rid, Deadline: time.Now().Add(time.Minute).Unix()}, privs[0], &lk))
	assert.Equal(t, nipcash.LookupDone, lk.Status)
	err = privateCall(t, token, constants.NIP47MethodCashWithdraw, map[string]any{"cancel": true, "request_id": rid}, privs[0], nil)
	requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
}
