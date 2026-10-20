//go:build integration

package integration

// Circle membership after a join (#143): a member removed from the circle has
// their wallet frozen, adding them back unfreezes it, and follow sets a hub
// must not trust admit no one. Runs over both policies, and for follow sets
// over a third-party provider's set and a set the hub publishes itself.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
)

// membershipAuditAmountMloki is what each frozen-wallet scenario funds and
// moves: small, but over the 1-loki floor every circle amount must clear.
const membershipAuditAmountMloki = 20_000

// randomFollowSetD gives each scenario its own set address, so runs and
// scenarios never read each other's versions on the shared relay.
func randomFollowSetD(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 4)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return prefix + "-" + hex.EncodeToString(b)
}

// publishProviderSet publishes a version of a third-party provider's follow
// set at an explicit time, so a later version always supersedes an earlier
// one even within the same second.
func publishProviderSet(t *testing.T, cfg *Config, providerPriv, d string, createdAt nostr.Timestamp, members ...string) nostr.Event {
	t.Helper()
	tags := nostr.Tags{{"d", d}}
	for _, m := range members {
		tags = append(tags, nostr.Tag{"p", m})
	}
	ev := nostr.Event{Kind: kindFollowSet, CreatedAt: createdAt, Tags: tags}
	require.NoError(t, ev.Sign(providerPriv))
	require.NoError(t, publishRaw(cfg, ev))
	return ev
}

// publishRaw sends ev to the test relay as-is, returning the relay's verdict.
func publishRaw(cfg *Config, ev nostr.Event) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relay, err := nostr.RelayConnect(ctx, cfg.RelayURL)
	if err != nil {
		return err
	}
	defer func() { _ = relay.Close() }()
	return relay.Publish(ctx, ev)
}

// createProviderFollowSetHub creates a follow_set hub reading providerPriv's
// set at d. The set must already be published.
func createProviderFollowSetHub(t *testing.T, cfg *Config, name, providerPriv, d string) (adminCreateAppResponse, *adminClient) {
	t.Helper()
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured - see integration/README.md")
	}
	opts := ephemeralCircleHubOpts{FundLoki: 1000}.withDefaults()
	resp := createEphemeralCircleHubApp(t, admin, adminCreateAppRequest{
		Name:                    ephemeralFixtureNamePrefix + " " + name,
		Scopes:                  []string{constants.CIRCLE_WALLET_SCOPE, constants.GET_INFO_SCOPE, constants.GET_BALANCE_SCOPE},
		Kind:                    "circle_hub",
		CircleIdentityName:      ephemeralFixtureNamePrefix + " " + name + " identity",
		CirclePolicy:            circlePolicyFollowSet,
		ProviderPubkey:          mustPubkey(t, providerPriv),
		FollowSetD:              d,
		CircleMaxExpSecs:        opts.MaxExpSecs,
		CirclePerWalletMaxMloki: opts.PerWalletMaxMloki,
	}, opts)
	return resp, admin
}

// refreshCircle asks the hub to read its follow set again now, as the
// dashboard's Sync button does, instead of waiting for the 5-minute refresh.
func refreshCircle(t *testing.T, admin *adminClient, hubID uint) {
	t.Helper()
	require.NoError(t, admin.do(http.MethodPost, fmt.Sprintf("/api/apps/%d/circle/refresh", hubID), nil))
}

// joinCircle joins hubConnection as memberPriv over the standard transport
// and connects to the issued wallet. The error is the join's refusal.
func joinCircle(t *testing.T, hubConnection, memberPriv string) (*nwcclient.Client, error) {
	t.Helper()
	hubClient := mustConnect(t, hubConnection)
	memberPub := mustPubkey(t, memberPriv)
	var result CreateCircleWalletResult
	err := hubClient.Call(ctxT(t), constants.NIP47MethodCreateCircleWallet, CreateCircleWalletParams{
		Pubkey:        memberPub,
		MaxAmount:     membershipAuditAmountMloki * 10,
		Expiry:        happyPathExpirySecs,
		BudgetRenewal: constants.BUDGET_RENEWAL_NEVER,
		IdentityEvent: eventJSON(t, buildCircleWalletIdentityEvent(t, memberPriv, hubClient.WalletPubkey())),
	}, &result)
	if err != nil {
		return nil, err
	}
	details, err := DecryptCircleWalletDetails(memberPriv, hubClient.WalletPubkey(), result)
	require.NoError(t, err)
	return mustConnect(t, CircleWalletURI(details, memberPub)), nil
}

func mustJoinCircle(t *testing.T, hubConnection, memberPriv string) *nwcclient.Client {
	t.Helper()
	child, err := joinCircle(t, hubConnection, memberPriv)
	require.NoError(t, err)
	return child
}

func nwcErrorCode(err error) string {
	var nwcErr *nwcclient.NWCError
	if errors.As(err, &nwcErr) {
		return nwcErr.Code
	}
	return ""
}

// requireFrozen asserts the wallet refuses everything that moves money while
// still answering reads, and that its balance is untouched.
func requireFrozen(t *testing.T, cfg *Config, child *nwcclient.Client, balanceMloki int64) {
	t.Helper()
	var invoice MakeInvoiceResult
	requireNWCErrorCode(t, child.Call(ctxT(t), "make_invoice", MakeInvoiceParams{Amount: 1000}, &invoice), constants.ERROR_RESTRICTED)

	payee := mintInvoiceFromSimpleWallet(t, cfg, 1000, "frozen circle wallet pays")
	var paid PayInvoiceResult
	requireNWCErrorCode(t, child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: payee.Invoice}, &paid), constants.ERROR_RESTRICTED)

	var balance GetBalanceResult
	require.NoError(t, child.Call(ctxT(t), "get_balance", struct{}{}, &balance), "a frozen wallet still shows its balance")
	assert.EqualValues(t, balanceMloki, balance.Balance, "freezing moves no money")
	var txs ListTransactionsResult
	require.NoError(t, child.Call(ctxT(t), "list_transactions", ListTransactionsParams{Limit: 10}, &txs), "and its history")
}

// requireWorking asserts the wallet can receive and spend. The invoice it
// makes is paid, so no incoming payment is left pending to block cleanup.
func requireWorking(t *testing.T, cfg *Config, child *nwcclient.Client) {
	t.Helper()
	fundCircleChild(t, cfg, child, 1000, "circle wallet receives")
	payee := mintInvoiceFromSimpleWallet(t, cfg, 1000, "circle wallet pays")
	var paid PayInvoiceResult
	require.NoError(t, child.Call(ctxT(t), "pay_invoice", PayInvoiceParams{Invoice: payee.Invoice}, &paid))
	require.NotEmpty(t, paid.Preimage)
}


func TestCircleMembershipAudit_AllowlistRemovalFreezesWallet(t *testing.T) {
	cfg := requireConfig(t)
	memberPriv := newTestPrivkey(t)
	memberPub := mustPubkey(t, memberPriv)
	hub, hubID, admin := createEphemeralCircleHub(t, cfg, "freeze-allowlist", circlePolicyAllowlist,
		[]string{memberPriv}, ephemeralCircleHubOpts{FundLoki: 1000})
	child := mustJoinCircle(t, hub.Connection, memberPriv)
	fundCircleChild(t, cfg, child, membershipAuditAmountMloki, "freeze-allowlist fund")

	requireWorking(t, cfg, child)
	balance := balanceOf(t, child)

	require.NoError(t, admin.removeCircleAllowlistMember(hubID, memberPub))
	requireFrozen(t, cfg, child, balance)

	require.NoError(t, admin.addCircleAllowlistMember(hubID, memberPub))
	requireWorking(t, cfg, child)
}

func TestCircleMembershipAudit_ProviderFollowSetRemovalFreezesWallet(t *testing.T) {
	cfg := requireConfig(t)
	memberPriv, otherPriv, providerPriv := newTestPrivkey(t), newTestPrivkey(t), newTestPrivkey(t)
	memberPub, otherPub := mustPubkey(t, memberPriv), mustPubkey(t, otherPriv)
	d := randomFollowSetD(t, "freeze")
	now := nostr.Now()

	publishProviderSet(t, cfg, providerPriv, d, now-30, memberPub, otherPub)
	resp, admin := createProviderFollowSetHub(t, cfg, "freeze-provider-set", providerPriv, d)
	child := mustJoinCircle(t, resp.PairingUri, memberPriv)
	fundCircleChild(t, cfg, child, membershipAuditAmountMloki, "freeze-provider-set fund")
	requireWorking(t, cfg, child)
	balance := balanceOf(t, child)

	publishProviderSet(t, cfg, providerPriv, d, now-20, otherPub)
	refreshCircle(t, admin, resp.ID)
	requireFrozen(t, cfg, child, balance)

	publishProviderSet(t, cfg, providerPriv, d, now-10, memberPub, otherPub)
	refreshCircle(t, admin, resp.ID)
	requireWorking(t, cfg, child)
}

// A hub-published set: public and hidden members alike are frozen when the
// hub drops them, and a hidden member may also join over the standard
// transport (NIP-CW leaves that privacy choice to the client).
func TestCircleMembershipAudit_ManagedFollowSetRemovalFreezesWallet(t *testing.T) {
	cfg := requireConfig(t)
	publicPriv, hiddenPriv := newTestPrivkey(t), newTestPrivkey(t)
	publicPub, hiddenPub := mustPubkey(t, publicPriv), mustPubkey(t, hiddenPriv)
	d := randomFollowSetD(t, "freeze-managed")

	resp, admin := createEphemeralManagedFollowSetHub(t, cfg, "freeze-managed-set", d, adminFollowSet{
		Title:         "Freeze audit",
		PublicMembers: []string{publicPub},
		HiddenMembers: []string{hiddenPub},
	}, ephemeralCircleHubOpts{FundLoki: 1000})
	identityID, err := admin.getCircleIdentityID(resp.ID)
	require.NoError(t, err)

	publicChild := mustJoinCircle(t, resp.PairingUri, publicPriv)
	hiddenChild, err := joinCircle(t, resp.PairingUri, hiddenPriv)
	require.NoError(t, err, "a hidden member is admitted over the standard transport too")
	fundCircleChild(t, cfg, publicChild, membershipAuditAmountMloki, "freeze-managed public fund")
	fundCircleChild(t, cfg, hiddenChild, membershipAuditAmountMloki, "freeze-managed hidden fund")

	require.NoError(t, admin.publishFollowSet(identityID, adminFollowSet{Title: "Freeze audit", HiddenMembers: []string{hiddenPub}}))
	requireFrozen(t, cfg, publicChild, balanceOf(t, publicChild))
	requireWorking(t, cfg, hiddenChild)

	require.NoError(t, admin.publishFollowSet(identityID, adminFollowSet{Title: "Freeze audit", PublicMembers: []string{publicPub}}))
	requireFrozen(t, cfg, hiddenChild, balanceOf(t, hiddenChild))
	requireWorking(t, cfg, publicChild)
}

// Follow sets the hub must not read: another author's set at the same
// address, the provider's own set under another d, and copies a relay can't
// accept. None admits an outsider; the real member still joins.
func TestCircleMembershipAudit_ForgedFollowSetsAdmitNoOne(t *testing.T) {
	cfg := requireConfig(t)
	memberPriv, outsiderPriv, providerPriv, attackerPriv := newTestPrivkey(t), newTestPrivkey(t), newTestPrivkey(t), newTestPrivkey(t)
	memberPub, outsiderPub, provider := mustPubkey(t, memberPriv), mustPubkey(t, outsiderPriv), mustPubkey(t, providerPriv)
	d := randomFollowSetD(t, "forged")
	now := nostr.Now()

	genuine := publishProviderSet(t, cfg, providerPriv, d, now-30, memberPub)
	resp, admin := createProviderFollowSetHub(t, cfg, "forged-sets", providerPriv, d)

	// Another author, same d, newer.
	publishProviderSet(t, cfg, attackerPriv, d, now-10, outsiderPub)
	// The provider's own set under a different d.
	publishProviderSet(t, cfg, providerPriv, d+"-other", now-10, outsiderPub)

	// Claims to be the provider but is signed by the attacker.
	spoofed := nostr.Event{Kind: kindFollowSet, CreatedAt: now - 5, Tags: nostr.Tags{{"d", d}, {"p", outsiderPub}}}
	require.NoError(t, spoofed.Sign(attackerPriv))
	spoofed.PubKey = provider
	spoofed.ID = spoofed.GetID()
	t.Logf("relay verdict on a set signed by another key: %v", publishRaw(cfg, spoofed))

	// The provider's real set under an id that isn't its hash.
	forgedID := genuine
	forgedID.ID = "0000000000000000000000000000000000000000000000000000000000000000"
	t.Logf("relay verdict on a set with a forged id: %v", publishRaw(cfg, forgedID))

	refreshCircle(t, admin, resp.ID)

	_, err := joinCircle(t, resp.PairingUri, outsiderPriv)
	requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
	mustJoinCircle(t, resp.PairingUri, memberPriv)
}

// The provider removed a member, then the hub restarted while relays only
// served the older version that still listed them. The member stays out.
//
// Needs INTEGRATION_RESTART_CMD: a command that stops the backend, empties
// the relay and starts both again, returning once the backend is up. The
// test then republishes only the older version.
func TestCircleMembershipAudit_RestartKeepsRemovedMemberOut(t *testing.T) {
	cfg := requireConfig(t)
	restartCmd := os.Getenv("INTEGRATION_RESTART_CMD")
	if restartCmd == "" {
		t.Skip("skipping: INTEGRATION_RESTART_CMD not set - see this test's doc comment")
	}
	removedPriv, keptPriv, providerPriv := newTestPrivkey(t), newTestPrivkey(t), newTestPrivkey(t)
	removedPub, keptPub := mustPubkey(t, removedPriv), mustPubkey(t, keptPriv)
	d := randomFollowSetD(t, "restart")
	now := nostr.Now()

	older := publishProviderSet(t, cfg, providerPriv, d, now-60, removedPub, keptPub)
	resp, admin := createProviderFollowSetHub(t, cfg, "restart-rollback", providerPriv, d)
	publishProviderSet(t, cfg, providerPriv, d, now-30, keptPub)
	refreshCircle(t, admin, resp.ID)
	_, err := joinCircle(t, resp.PairingUri, removedPriv)
	requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)

	restartCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(restartCtx, "sh", "-c", restartCmd).CombinedOutput() //nolint:gosec // the operator's own opt-in hook
	require.NoError(t, err, "restart command failed: %s", out)
	require.NoError(t, publishRaw(cfg, older), "the emptied relay takes the older version")

	// The first answer after startup may be NOT_READY while the hub warms up.
	deadline := time.Now().Add(90 * time.Second)
	for {
		_, err = joinCircle(t, resp.PairingUri, removedPriv)
		if nwcErrorCode(err) != "NOT_READY" || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
	mustJoinCircle(t, resp.PairingUri, keptPriv)
}
