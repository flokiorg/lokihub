//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// End-to-end checks for the circle audit follow-ups (#148): the cap floor,
// Delete All moving money to the main balance, the frozen flag in the
// hub's wallet list, and all-or-nothing hub settings updates.

type followupsCircleChild struct {
	AppID           uint   `json:"appId"`
	RequesterPubkey string `json:"requesterPubkey"`
	BalanceMloki    int64  `json:"balanceMloki"`
	Frozen          *bool  `json:"frozen"`
}

func listFollowupsCircleChildren(t *testing.T, admin *adminClient, hubID uint) []followupsCircleChild {
	t.Helper()
	var resp struct {
		Children []followupsCircleChild `json:"children"`
	}
	require.NoError(t, admin.do(http.MethodGet, fmt.Sprintf("/api/apps/%d/circle/children?limit=0", hubID), &resp))
	return resp.Children
}

func frozenOf(t *testing.T, admin *adminClient, hubID uint, member string) *bool {
	t.Helper()
	for _, child := range listFollowupsCircleChildren(t, admin, hubID) {
		if child.RequesterPubkey == member {
			return child.Frozen
		}
	}
	require.Failf(t, "member wallet not listed", "member %s has no wallet under hub %d", member, hubID)
	return nil
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
			requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)

			child, err := joinWithCap(t, hub, member, constants.MIN_CIRCLE_WALLET_MAX_MLOKI)
			require.NoError(t, err)
			var budget GetBudgetResult
			require.NoError(t, child.Call(ctxT(t), "get_budget", struct{}{}, &budget))
			assert.EqualValues(t, constants.MIN_CIRCLE_WALLET_MAX_MLOKI, budget.TotalBudget)
		})
	}
}

// The hub's wallet list shows a wallet frozen once its member leaves, and
// working again once they are back.
func TestCircleFollowups_FrozenFlagFollowsMembership(t *testing.T) {
	cfg := requireConfig(t)
	member := newTestPrivkey(t)
	memberPub := mustPubkey(t, member)
	hub, hubID, admin := createEphemeralCircleHub(t, cfg, "followups-frozen-flag", circlePolicyAllowlist, []string{member},
		ephemeralCircleHubOpts{FundLoki: 1_000})
	_ = mintCircleChildFor(t, hub, member)

	frozen := frozenOf(t, admin, hubID, memberPub)
	require.NotNil(t, frozen)
	assert.False(t, *frozen)

	require.NoError(t, admin.removeCircleAllowlistMember(hubID, memberPub))
	frozen = frozenOf(t, admin, hubID, memberPub)
	require.NotNil(t, frozen)
	assert.True(t, *frozen, "a member who left has a frozen wallet")

	require.NoError(t, admin.addCircleAllowlistMember(hubID, memberPub))
	frozen = frozenOf(t, admin, hubID, memberPub)
	require.NotNil(t, frozen)
	assert.False(t, *frozen, "adding them back unfreezes it")
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

// Delete All moves a funded member wallet and the hub's own balance to the
// main balance as a recorded transfer, then deletes both.
func TestCircleFollowups_DeleteAllSweepsToMainBalance(t *testing.T) {
	cfg := requireConfig(t)
	member := newTestPrivkey(t)
	const hubFundLoki = 1_000
	hub, hubID, admin := createEphemeralCircleHub(t, cfg, "followups-delete-all", circlePolicyAllowlist, []string{member},
		ephemeralCircleHubOpts{FundLoki: hubFundLoki})
	child := mintCircleChildFor(t, hub, member)
	const childFundMloki = 7_000
	fundCircleChild(t, cfg, child, childFundMloki, "followups delete-all (fund)")
	require.Eventually(t, func() bool { return balanceOf(t, child) == childFundMloki }, 10*time.Second, 200*time.Millisecond)
	before := len(mainBalanceTransfersIn(t, admin))

	var result struct {
		HubDeleted      bool   `json:"hubDeleted"`
		DeletedChildIDs []uint `json:"deletedChildIds"`
		SkippedChildIDs []uint `json:"skippedChildIds"`
	}
	require.NoError(t, admin.doBody(http.MethodPost, fmt.Sprintf("/api/apps/%d/circle/delete", hubID),
		map[string]string{"mode": "all"}, &result))
	assert.True(t, result.HubDeleted)
	assert.Len(t, result.DeletedChildIDs, 1)
	assert.Empty(t, result.SkippedChildIDs)

	transfers := mainBalanceTransfersIn(t, admin)
	require.Len(t, transfers, before+1, "one recorded transfer into the main balance")
	assert.EqualValues(t, hubFundLoki*1000+childFundMloki, transfers[0].Amount,
		"the hub's balance, including what the wallet held, reaches the main balance")
	assert.Equal(t, "settled", transfers[0].State)

	var app map[string]interface{}
	err := admin.do(http.MethodGet, fmt.Sprintf("/api/apps/%d", hubID), &app)
	assert.Error(t, err, "the hub is gone")
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
