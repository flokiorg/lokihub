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
	return budget.TotalBudget - uint64(balanceOf(t, c)) //nolint:gosec // a wallet balance is never negative
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

	t.Run("HoldInvoice_AboveCap_QuotaExceeded", func(t *testing.T) {
		var result MakeInvoiceResult
		err := a.Call(ctxT(t), "make_hold_invoice", map[string]any{
			"amount":       roomOf(t, a) + 1_000,
			"description":  "integration circle audit hold cap",
			"payment_hash": newTestPrivkey(t), // any 32 random bytes in hex
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_QUOTA_EXCEEDED)
	})

	t.Run("ZeroAmountInvoice_Refused", func(t *testing.T) {
		var result MakeInvoiceResult
		err := a.Call(ctxT(t), "make_invoice", MakeInvoiceParams{Description: "integration circle audit zero amount"}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("MemberWallet_CannotCreateCircleWallets", func(t *testing.T) {
		before, err := admin.listCircleChildren(hubID)
		require.NoError(t, err)

		stranger := newTestPrivkey(t)
		var result CreateCircleWalletResult
		err = a.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
			Pubkey:        mustPubkey(t, stranger),
			MaxAmount:     1_000,
			Expiry:        happyPathExpirySecs,
			IdentityEvent: eventJSON(t, buildCircleWalletIdentityEvent(t, stranger, a.WalletPubkey())),
		}, &result)
		// A member's wallet has no circle_wallet scope, so the request never
		// reaches the join handler.
		requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)

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

	// A keysend names its recipient app by ID, which anyone can write. Sent
	// from this node to a member wallet with no room, it is refused before it
	// leaves; one that fits is credited.
	t.Run("Keysend_ToMemberWallet_HeldToItsCap", func(t *testing.T) {
		var info GetInfoResult
		require.NoError(t, a.Call(ctxT(t), "get_info", struct{}{}, &info))
		require.NotNil(t, info.Pubkey)
		bID, err := admin.findAppIDByWalletPubkey(b.WalletPubkey())
		require.NoError(t, err)
		namesB := []tlvRecord{{Type: customKeyTLVType, Value: hex.EncodeToString([]byte(strconv.FormatUint(uint64(bID), 10)))}}

		// Fill B nearly to its cap, and give A more than B's remaining room.
		if room := roomOf(t, b); room > 5_000 {
			fundCircleChild(t, cfg, b, room-4_000, "integration circle audit fill B")
		}
		if balanceOf(t, a) < 20_000 {
			fundCircleChild(t, cfg, a, 20_000, "integration circle audit fund A for keysend")
		}
		room := roomOf(t, b)
		require.Greater(t, balanceOf(t, a), int64(room+1_000), "A needs more than B's room for this to mean anything")
		bBefore := balanceOf(t, b)
		err = a.Call(ctxT(t), "pay_keysend", payKeysendParams{Amount: room + 1_000, Pubkey: *info.Pubkey, TLVRecords: namesB}, &PayInvoiceResult{})
		requireNWCErrorCode(t, err, constants.ERROR_QUOTA_EXCEEDED)
		assert.Equal(t, bBefore, balanceOf(t, b))

		require.NoError(t, a.Call(ctxT(t), "pay_keysend", payKeysendParams{Amount: 1_000, Pubkey: *info.Pubkey, TLVRecords: namesB}, &PayInvoiceResult{}))
		assert.Equal(t, bBefore+1_000, balanceOf(t, b))
	})
}

// "Empty wallets only" means nothing can still arrive or leave: a member
// wallet with a live invoice is kept, and so is the hub.
func TestCircleMoneyAudit_HubDeleteEmptyOnly_KeepsWalletWithLiveInvoice(t *testing.T) {
	cfg := requireConfig(t)
	member := newTestPrivkey(t)
	hub, hubID, admin := createEphemeralCircleHub(t, cfg, "money-audit-delete", circlePolicyAllowlist,
		[]string{member}, ephemeralCircleHubOpts{FundLoki: 1_000})
	child := mintCircleChild(t, hub)

	var invoice MakeInvoiceResult
	require.NoError(t, child.Call(ctxT(t), "make_invoice", MakeInvoiceParams{Amount: 1_000, Description: "integration circle audit live invoice"}, &invoice))

	var result struct {
		HubDeleted      bool   `json:"hubDeleted"`
		SkippedChildIDs []uint `json:"skippedChildIds"`
	}
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/apps/%d/circle/delete", hubID),
		map[string]string{"mode": "empty_only"}, &result))
	assert.False(t, result.HubDeleted, "the hub stays while a member wallet has a live invoice")
	assert.Len(t, result.SkippedChildIDs, 1)

	// Settle the invoice so cleanup can reclaim and delete the wallet now
	// rather than after the invoice lapses.
	payInvoiceFromSimpleWallet(t, cfg, invoice.Invoice)
}

// Hidden follow-set members are expected to join privately, but the hub does
// not refuse one that uses the standard transport: privacy is the client's
// choice (nmilat's RequirePrivate). Pinned so a change is deliberate.
func TestCircleMoneyAudit_HiddenMemberOverStandardTransport_Admitted(t *testing.T) {
	cfg := requireConfig(t)
	hiddenPriv := newTestPrivkey(t)
	d := "e2e-hidden-std-" + newTestPrivkey(t)[:8]
	resp, _ := createEphemeralManagedFollowSetHub(t, cfg, "money-audit-hidden-std", d, adminFollowSet{
		Title:         "E2E hidden member, standard transport",
		HiddenMembers: []string{mustPubkey(t, hiddenPriv)},
	}, ephemeralCircleHubOpts{})

	client, err := cwclient.Connect(ctxT(t), resp.PairingUri)
	require.NoError(t, err)
	t.Cleanup(client.Close)
	joined, err := client.CreateCircleWallet(ctxT(t), joinParams(hiddenPriv))
	require.NoError(t, err)
	assert.NotEmpty(t, joined.WalletPubkey)
}
