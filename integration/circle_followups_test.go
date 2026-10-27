//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ohstr/nmilat/nipcw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// End-to-end checks for the circle audit follow-ups (#148): the cap floor,
// closing a circle (teardown, no sweep), each wallet's state in the hub's
// wallet list, and all-or-nothing hub settings updates.

type followupsCircleChild struct {
	AppID           uint   `json:"appId"`
	RequesterPubkey string `json:"requesterPubkey"`
	BalanceMloki    int64  `json:"balanceMloki"`
	State           string `json:"state"`
	Frozen          bool   `json:"frozen"`
}

func listFollowupsCircleChildren(t *testing.T, admin *adminClient, hubID uint) []followupsCircleChild {
	t.Helper()
	var resp struct {
		Children []followupsCircleChild `json:"children"`
	}
	require.NoError(t, admin.do(http.MethodGet, fmt.Sprintf("/api/apps/%d/circle/children?limit=0", hubID), &resp))
	return resp.Children
}

func stateOf(t *testing.T, admin *adminClient, hubID uint, member string) string {
	t.Helper()
	for _, child := range listFollowupsCircleChildren(t, admin, hubID) {
		if child.RequesterPubkey == member {
			return child.State
		}
	}
	require.Failf(t, "member wallet not listed", "member %s has no wallet under hub %d", member, hubID)
	return ""
}

// joinWithCap asks hub for a wallet capped at maxAmountMloki. Each call signs a
// distinct proof: a refused join still uses up the one it sent.
func joinWithCap(t *testing.T, hub CircleHubConfig, memberPriv string, maxAmountMloki uint64) (*nwcclient.Client, error) {
	t.Helper()
	hubClient := mustConnect(t, hub.Connection)
	memberPub := mustPubkey(t, memberPriv)
	var result CreateCircleWalletResult
	err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        memberPub,
		MaxAmount:     maxAmountMloki,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, distinctCircleWalletIdentityEvent(t, memberPriv, hubClient.WalletPubkey(), fmt.Sprintf("cap-%d", maxAmountMloki))),
	}, &result)
	if err != nil {
		return nil, err
	}
	details, err := DecryptCircleWalletDetails(memberPriv, hubClient.WalletPubkey(), result)
	require.NoError(t, err)
	return mustConnectCircleWallet(t, CircleWalletConnection(details, memberPub)), nil
}

// A cap the 10-loki fee reserve would swallow is refused; the floor itself
// is accepted.
func TestCircleFollowups_JoinCapFloor(t *testing.T) {
	cfg := requireConfig(t)
	for _, policy := range []string{circlePolicyAllowlist, circlePolicyFollowSet} {
		t.Run(policy, func(t *testing.T) {
			member := newTestPrivkey(t)
			hub, _, _ := createEphemeralCircleHub(t, cfg, "followups-cap-floor-"+policy, policy, []string{member},
				ephemeralCircleHubOpts{FundLoki: 1_000})

			_, err := joinWithCap(t, hub, member, constants.MIN_CIRCLE_WALLET_MAX_MLOKI-1)
			requireNWCErrorCode(t, err, nipcw.ErrLimitExceeded)

			child, err := joinWithCap(t, hub, member, constants.MIN_CIRCLE_WALLET_MAX_MLOKI)
			require.NoError(t, err)
			var budget GetBudgetResult
			require.NoError(t, child.Call(ctxT(t), "get_budget", struct{}{}, &budget))
			assert.EqualValues(t, constants.MIN_CIRCLE_WALLET_MAX_MLOKI, budget.TotalBudget)
		})
	}
}

// The hub's wallet list shows a wallet Suspended once its member leaves (an
// allowlist edit is a resolution, applied at once), and Active again once they
// are back within its grace (NIP-CW §Membership).
func TestCircleFollowups_StateFollowsMembership(t *testing.T) {
	cfg := requireConfig(t)
	member := newTestPrivkey(t)
	memberPub := mustPubkey(t, member)
	hub, hubID, admin := createEphemeralCircleHub(t, cfg, "followups-frozen-flag", circlePolicyAllowlist, []string{member},
		ephemeralCircleHubOpts{FundLoki: 1_000})
	_ = mintCircleChildFor(t, hub, member)

	assert.Equal(t, "active", stateOf(t, admin, hubID, memberPub))

	require.NoError(t, admin.removeCircleAllowlistMember(hubID, memberPub))
	assert.Equal(t, "suspended", stateOf(t, admin, hubID, memberPub), "a member who left has a Suspended wallet")

	require.NoError(t, admin.addCircleAllowlistMember(hubID, memberPub))
	assert.Equal(t, "active", stateOf(t, admin, hubID, memberPub), "adding them back within the grace makes it Active again")
}

type followupsTransaction struct {
	Type        string `json:"type"`
	State       string `json:"state"`
	Amount      uint64 `json:"amount"`
	Description string `json:"description"`
	AppId       *uint  `json:"appId"`
}

// mainBalanceTransfersIn lists settled internal transfers into the main
// balance (no app), newest first.
func mainBalanceTransfersIn(t *testing.T, admin *adminClient) []followupsTransaction {
	t.Helper()
	var resp struct {
		Transactions []followupsTransaction `json:"transactions"`
	}
	require.NoError(t, admin.do(http.MethodGet, "/api/transactions?limit=100", &resp))
	var out []followupsTransaction
	for _, tx := range resp.Transactions {
		if tx.AppId == nil && tx.Type == constants.TRANSACTION_TYPE_INCOMING && tx.Description == "transfer" {
			out = append(out, tx)
		}
	}
	return out
}

// Closing a circle tears it down (NIP-CW §Lifecycle and Deletion): the funded
// member wallet moves to Closing and keeps its balance, nothing reaches the
// main balance, the hub stays while it has a wallet and takes no new joins.
func TestCircleFollowups_CloseCircleKeepsBalances(t *testing.T) {
	cfg := requireConfig(t)
	member := newTestPrivkey(t)
	memberPub := mustPubkey(t, member)
	hub, hubID, admin := createEphemeralCircleHub(t, cfg, "followups-delete-all", circlePolicyAllowlist, []string{member},
		ephemeralCircleHubOpts{FundLoki: 1_000})
	child := mintCircleChildFor(t, hub, member)
	const childFundMloki = 7_000
	fundCircleChild(t, cfg, child, childFundMloki, "followups close circle (fund)")
	require.Eventually(t, func() bool { return balanceOf(t, child) == childFundMloki }, 10*time.Second, 200*time.Millisecond)
	before := len(mainBalanceTransfersIn(t, admin))

	var result struct {
		HubDeleted      bool   `json:"hubDeleted"`
		ClosingChildIDs []uint `json:"closingChildIds"`
	}
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/apps/%d/circle/delete", hubID),
		map[string]string{"mode": "all"}, &result))
	assert.False(t, result.HubDeleted, "the hub stays while it has a wallet")
	assert.Len(t, result.ClosingChildIDs, 1)

	assert.Equal(t, "closing", stateOf(t, admin, hubID, memberPub))
	assert.EqualValues(t, childFundMloki, balanceOf(t, child), "the member's balance is never swept")
	assert.Len(t, mainBalanceTransfersIn(t, admin), before, "nothing moved to the main balance")

	// A Closing wallet still pays out.
	invoice := mintInvoiceFromSimpleWallet(t, cfg, 1_000, "followups close circle (payout)")
	var paid PayInvoiceResult
	require.NoError(t, child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: invoice.Invoice}, &paid))
}

// A rejected hub setting leaves the whole update unsaved.
func TestCircleFollowups_HubSettingsUpdateIsAllOrNothing(t *testing.T) {
	cfg := requireConfig(t)
	_, hubID, admin := createEphemeralCircleHub(t, cfg, "followups-settings-atomic", circlePolicyAllowlist,
		[]string{newTestPrivkey(t)}, ephemeralCircleHubOpts{FundLoki: 1_000})

	var before struct {
		Name string `json:"name"`
	}
	require.NoError(t, admin.do(http.MethodGet, fmt.Sprintf("/api/apps/%d", hubID), &before))

	err := admin.doBody(http.MethodPatch, fmt.Sprintf("/api/apps/%d", hubID), map[string]interface{}{
		"name":          before.Name + " renamed",
		"circleFeesPpm": constants.MAX_FEES_PPM + 1,
	}, nil)
	require.Error(t, err)

	var after struct {
		Name string `json:"name"`
	}
	require.NoError(t, admin.do(http.MethodGet, fmt.Sprintf("/api/apps/%d", hubID), &after))
	assert.Equal(t, before.Name, after.Name, "the name change from the rejected request is not saved")
}
