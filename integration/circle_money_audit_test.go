//go:build integration

// circle_money_audit_test.go drives the money-flow findings of the circle
// wallet audit end to end, over a real relay against a live hub: every way a
// member's wallet can receive must respect its cap, a wallet can't mint
// wallets, same-node payments between members carry no fee skim, and an
// "empty wallets only" hub delete keeps a wallet that still has a live invoice.
package integration

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	cwclient "github.com/ohstr/nmilat/nipcw/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// moneyAuditFeesPpm is a 1% skim, so a payment that wrongly paid one would show.
const moneyAuditFeesPpm = 10_000

// mintCircleChildFor is mintCircleChild for a specific authorized member.
func mintCircleChildFor(t *testing.T, hub CircleHubConfig, memberPriv string) *nwcclient.Client {
	t.Helper()
	one := hub
	one.Members.AuthorizedPrivkeys = []string{memberPriv}
	return mintCircleChild(t, one)
}

// roomOf is how much more c may receive: its cap less its balance. Only
// accurate while c has no unpaid invoices.
func roomOf(t *testing.T, c *nwcclient.Client) uint64 {
	t.Helper()
	var budget GetBudgetResult
	require.NoError(t, c.Call(ctxT(t), "get_budget", struct{}{}, &budget))
	require.NotNil(t, budget.TotalReceiveBudget, "a circle wallet reports its receive cap")
	// NIP-CW §What a Circle Wallet Connection Allows: the receive room is the
	// receive cap less what counts against it this period.
	return *budget.TotalReceiveBudget - *budget.UsedReceiveBudget
}

type payKeysendParams struct {
	Amount     uint64      `json:"amount"`
	Pubkey     string      `json:"pubkey"`
	TLVRecords []tlvRecord `json:"tlv_records"`
}

type tlvRecord struct {
	Type  uint64 `json:"type"`
	Value string `json:"value"`
}

// customKeyTLVType is the keysend record naming the recipient app by ID
// (transactions.CustomKeyTlvType).
const customKeyTLVType = 696969

func TestCircleMoneyAudit(t *testing.T) {
	cfg := requireConfig(t)
	memberA, memberB := newTestPrivkey(t), newTestPrivkey(t)
	hub, hubID, admin := createEphemeralCircleHub(t, cfg, "money-audit", circlePolicyAllowlist,
		[]string{memberA, memberB}, ephemeralCircleHubOpts{FundLoki: 1_000, FeesPpm: moneyAuditFeesPpm})
	a := mintCircleChildFor(t, hub, memberA)
	b := mintCircleChildFor(t, hub, memberB)

	// A member cannot issue a hold invoice at all (NIP-CW §What a Circle Wallet
	// Connection Allows), whatever its amount.
	t.Run("HoldInvoice_Restricted", func(t *testing.T) {
		var result MakeInvoiceResult
		err := a.Call(ctxT(t), "make_hold_invoice", map[string]any{
			"amount":       1_000,
			"description":  "integration circle audit hold",
			"payment_hash": newTestPrivkey(t), // any 32 random bytes in hex
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
	})

	t.Run("ZeroAmountInvoice_Refused", func(t *testing.T) {
		var result MakeInvoiceResult
		err := a.Call(ctxT(t), "make_invoice", MakeInvoiceParams{Description: "integration circle audit zero amount"}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
	})

	t.Run("MemberWallet_CannotCreateCircleWallets", func(t *testing.T) {
		before, err := admin.listCircleChildren(hubID)
		require.NoError(t, err)

		stranger := newTestPrivkey(t)
		var result CreateCircleWalletResult
		err = a.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        mustPubkey(t, stranger),
			MaxAmount:     circleHappyPathAmountMloki,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: eventJSON(t, buildCircleWalletIdentityEvent(t, stranger, a.WalletPubkey())),
		}, &result)
		// Joins are served on the private transport only: kind 23194 refuses
		// one before reading anything.
		requireNWCErrorCode(t, err, constants.ERROR_NOT_IMPLEMENTED)

		after, err := admin.listCircleChildren(hubID)
		require.NoError(t, err)
		assert.Len(t, after, len(before))
	})

	// On one node, a member paying another member's invoice is a same-node
	// payment: the payer pays exactly the amount, with no fee skim.
	t.Run("SiblingPayment_NoSkim", func(t *testing.T) {
		const amount = 2_000
		fundCircleChild(t, cfg, a, 20_000, "integration circle audit fund A")
		var invoice MakeInvoiceResult
		require.NoError(t, b.Call(ctxT(t), "make_invoice", MakeInvoiceParams{Amount: amount, Description: "integration circle audit sibling"}, &invoice))

		aBefore, bBefore := balanceOf(t, a), balanceOf(t, b)
		var paid PayInvoiceResult
		require.NoError(t, a.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: invoice.Invoice}, &paid))
		assert.Equal(t, aBefore-amount, balanceOf(t, a), "the payer pays the amount and no skim")
		assert.Equal(t, bBefore+amount, balanceOf(t, b))
	})

	// A keysend names its recipient app by ID, which anyone can write. From
	// another member of the circle it is a same-circle transfer, counted on no
	// cap; from any other app on this node it is external, and one larger than
	// the wallet's receive room is refused before it leaves (NIP-CW §Own Funds
	// and Caps); one that fits is credited.
	t.Run("Keysend_ToMemberWallet_HeldToItsReceiveRoom", func(t *testing.T) {
		var info GetInfoResult
		require.NoError(t, a.Call(ctxT(t), "get_info", struct{}{}, &info))
		require.NotNil(t, info.Pubkey)
		bID, err := admin.findAppIDByWalletPubkey(b.WalletPubkey())
		require.NoError(t, err)
		namesB := []tlvRecord{{Type: customKeyTLVType, Value: hex.EncodeToString([]byte(strconv.FormatUint(uint64(bID), 10)))}}

		// Leave B 1,000 of receive room, then send it more than that.
		if room := roomOf(t, b); room > 1_000 {
			fundCircleChild(t, cfg, b, room-1_000, "integration circle audit fill B")
		}
		require.EqualValues(t, 1_000, roomOf(t, b))
		if balanceOf(t, a) < 2_000 {
			fundCircleChild(t, cfg, a, 2_000, "integration circle audit fund A for keysend")
		}
		bBefore := balanceOf(t, b)
		require.NoError(t, a.Call(ctxT(t), "pay_keysend", payKeysendParams{Amount: 2_000, Pubkey: *info.Pubkey, TLVRecords: namesB}, &PayInvoiceResult{}),
			"a same-circle keysend counts on no cap")
		assert.Equal(t, bBefore+2_000, balanceOf(t, b))
		assert.EqualValues(t, 1_000, roomOf(t, b), "and leaves the receive room as it was")

		outsider := mustConnect(t, createEphemeralSimpleWallet(t, cfg).Connection)
		bBefore = balanceOf(t, b)
		err = outsider.Call(ctxT(t), "pay_keysend", payKeysendParams{Amount: 2_000, Pubkey: *info.Pubkey, TLVRecords: namesB}, &PayInvoiceResult{})
		requireNWCErrorCode(t, err, constants.ERROR_QUOTA_EXCEEDED)
		assert.Equal(t, bBefore, balanceOf(t, b))

		require.NoError(t, outsider.Call(ctxT(t), "pay_keysend", payKeysendParams{Amount: 1_000, Pubkey: *info.Pubkey, TLVRecords: namesB}, &PayInvoiceResult{}))
		assert.Equal(t, bBefore+1_000, balanceOf(t, b))
		assert.Zero(t, roomOf(t, b), "an external receipt counts on the receive cap")
	})
}

// Closing a circle moves a member wallet with a live invoice to Closing, which
// cancels the invoice: a later payment of it fails (NIP-CW §Own Funds and Caps:
// only Active wallets receive).
func TestCircleMoneyAudit_CloseCircle_CancelsLiveInvoices(t *testing.T) {
	cfg := requireConfig(t)
	member := newTestPrivkey(t)
	hub, hubID, admin := createEphemeralCircleHub(t, cfg, "money-audit-delete", circlePolicyAllowlist,
		[]string{member}, ephemeralCircleHubOpts{FundLoki: 1_000})
	child := mintCircleChild(t, hub)

	var invoice MakeInvoiceResult
	require.NoError(t, child.Call(ctxT(t), "make_invoice", MakeInvoiceParams{Amount: 1_000, Description: "integration circle audit live invoice"}, &invoice))

	var result struct {
		HubDeleted      bool   `json:"hubDeleted"`
		ClosingChildIDs []uint `json:"closingChildIds"`
	}
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/apps/%d/circle/delete", hubID),
		map[string]string{"mode": "empty_only"}, &result))
	assert.False(t, result.HubDeleted, "the hub stays while it has a wallet")
	assert.Len(t, result.ClosingChildIDs, 1)

	payer := mustConnect(t, createEphemeralSimpleWallet(t, cfg).Connection)
	var paid PayInvoiceResult
	assert.Error(t, payer.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: invoice.Invoice}, &paid),
		"a Closing wallet's invoice was cancelled")
	assert.Zero(t, balanceOf(t, child))
}

// A hidden follow-set member joins over the private transport, the only one that
// serves joins (NIP-CW §Private Join).
func TestCircleMoneyAudit_HiddenMemberJoinsPrivately(t *testing.T) {
	cfg := requireConfig(t)
	hiddenPriv := newTestPrivkey(t)
	d := "e2e-hidden-std-" + newTestPrivkey(t)[:8]
	resp, _ := createEphemeralManagedFollowSetHub(t, cfg, "money-audit-hidden-std", d, adminFollowSet{
		Title:         "E2E hidden member, private join",
		HiddenMembers: []string{mustPubkey(t, hiddenPriv)},
	}, ephemeralCircleHubOpts{})

	client, err := cwclient.Connect(ctxT(t), resp.CircleHubToken, cwclient.Options{})
	require.NoError(t, err)
	t.Cleanup(client.Close)
	joined, err := client.Join(ctxT(t), joinParams(hiddenPriv), joinOpts, &memJoinJournal{})
	require.NoError(t, err)
	assert.NotEmpty(t, joined.WalletPubkey)
}
