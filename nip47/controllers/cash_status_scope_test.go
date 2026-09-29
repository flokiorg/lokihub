package controllers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// cash_status' scope, per NIP-CASH §Scoping the Roster. Four rules, and the
// interesting part is that the DEFAULT differs by transport, because the two
// differ in what they can know:
//
//	private  + absent -> mine      (the safe default: learn nothing about co-recipients)
//	private  + all    -> all
//	standard + absent -> all       (unchanged: the Hub cannot identify the caller)
//	standard + mine   -> REJECTED  (cannot be honoured, must not be approximated)

// scopeFixture builds a two-recipient bill and returns the wallet plus both
// recipients' pubkeys.
func scopeFixture(t *testing.T) (svc *tests.TestService, wallet *db.App, mine, theirs string) {
	t.Helper()
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	t.Cleanup(svc.Remove)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet = newFundedCashWallet(t, svc, hub, 3000)

	mine, _ = nostr.GetPublicKey(nostr.GeneratePrivateKey())
	theirs, _ = nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: mine, AmountMloki: 1000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: theirs, AmountMloki: 2000},
	}))
	return svc, wallet, mine, theirs
}

func statusRequest(t *testing.T, scope string) *models.Request {
	t.Helper()
	req := &models.Request{Method: constants.NIP47MethodCashStatus}
	if scope != "" {
		raw, err := json.Marshal(nipcash.CashStatusParams{Scope: scope})
		require.NoError(t, err)
		req.Params = raw
	}
	return req
}

func callStatus(t *testing.T, svc *tests.TestService, wallet *db.App, req *models.Request, caller *CashStatusCaller) *models.Response {
	t.Helper()
	var response *models.Response
	NewTestNip47Controller(svc).HandleCashStatusEvent(context.TODO(), req, 1, wallet, func(r *models.Response, _ nostr.Tags) {
		response = r
	}, caller)
	require.NotNil(t, response)
	return response
}

// TestCashStatusScope_PrivateAbsentMeansMine is the rule that actually changes
// behaviour, and the one with a privacy consequence: before this, a private
// caller who said nothing received every co-recipient's identity, amount and
// claim state — the exact disclosure the private transport exists to prevent,
// as the default.
func TestCashStatusScope_PrivateAbsentMeansMine(t *testing.T) {
	svc, wallet, mine, theirs := scopeFixture(t)

	response := callStatus(t, svc, wallet, statusRequest(t, ""), &CashStatusCaller{IdentityValue: mine})
	require.Nil(t, response.Error)
	result := response.Result.(nipcash.CashStatusResult)

	require.Len(t, result.Recipients, 1, "an unscoped private read must return only the caller's own row")
	assert.Equal(t, mine, result.Recipients[0].IdentityValue)
	assert.Equal(t, uint64(1000), result.Recipients[0].AmountMillis)
	for _, r := range result.Recipients {
		assert.NotEqual(t, theirs, r.IdentityValue, "a co-recipient's row leaked into a scoped read")
	}
}

// TestCashStatusScope_PrivateAllIsStillAvailable — scoping is a default, not a
// removal. A caller who genuinely wants the shared roster can still ask.
func TestCashStatusScope_PrivateAllIsStillAvailable(t *testing.T) {
	svc, wallet, mine, _ := scopeFixture(t)

	response := callStatus(t, svc, wallet, statusRequest(t, nipcash.ScopeAll), &CashStatusCaller{IdentityValue: mine})
	require.Nil(t, response.Error)
	result := response.Result.(nipcash.CashStatusResult)
	assert.Len(t, result.Recipients, 2, "scope=all must still return the full roster")
}

// TestCashStatusScope_StandardAbsentMeansAll pins that the standard transport is
// untouched. Every recipient holds the same connection string there, so the
// shared roster is the only answer the Hub can give — and every existing client
// depends on it.
func TestCashStatusScope_StandardAbsentMeansAll(t *testing.T) {
	svc, wallet, _, _ := scopeFixture(t)

	response := callStatus(t, svc, wallet, statusRequest(t, ""), nil)
	require.Nil(t, response.Error)
	result := response.Result.(nipcash.CashStatusResult)
	assert.Len(t, result.Recipients, 2, "the standard transport's unscoped read must be unchanged")
}

// TestCashStatusScope_StandardRejectsMine covers the rule that is easy to get
// wrong in the harmful direction. The Hub cannot identify the caller on this
// transport, so it cannot honour "mine" — and silently answering "all" instead
// would return far more than was asked for, to a caller who had explicitly asked
// for less.
func TestCashStatusScope_StandardRejectsMine(t *testing.T) {
	svc, wallet, _, _ := scopeFixture(t)

	response := callStatus(t, svc, wallet, statusRequest(t, nipcash.ScopeMine), nil)
	require.NotNil(t, response.Error, "scope=mine on the standard transport must be refused, not approximated")
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
	assert.Nil(t, response.Result, "a refused request must not also return a roster")
}

func TestCashStatusScope_UnknownScopeRejected(t *testing.T) {
	svc, wallet, mine, _ := scopeFixture(t)

	response := callStatus(t, svc, wallet, statusRequest(t, "everything"), &CashStatusCaller{IdentityValue: mine})
	require.NotNil(t, response.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code)
}

// TestCashStatusScope_MineForANonRecipientReturnsNothing covers the no-match
// case. A proof can verify without its signer holding a slice of this
// particular bill, and widening the answer on no match would turn every
// mismatch into a full roster disclosure — the opposite of what scoping is for.
func TestCashStatusScope_MineForANonRecipientReturnsNothing(t *testing.T) {
	svc, wallet, _, _ := scopeFixture(t)
	stranger, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	response := callStatus(t, svc, wallet, statusRequest(t, nipcash.ScopeMine), &CashStatusCaller{IdentityValue: stranger})
	require.Nil(t, response.Error)
	result := response.Result.(nipcash.CashStatusResult)
	assert.Empty(t, result.Recipients, "a non-recipient must get no rows, never the full roster")
}

// TestCashStatusScope_CashModeBillScopesToItsCashRows documents an honest limit.
// A cash-mode slice has no identity of its own — the secret in params is the
// whole authorization — so at this layer the Hub cannot attribute one cash row to
// one caller. "mine" therefore returns the bill's cash rows. That discloses no
// identity, because cash rows carry none, and it is the conservative answer
// rather than a pretence of per-slice attribution.
func TestCashStatusScope_CashModeBillScopesToItsCashRows(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 3000)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityCash, AmountMloki: 3000},
	}))

	response := callStatus(t, svc, wallet, statusRequest(t, nipcash.ScopeMine), &CashStatusCaller{IsCash: true})
	require.Nil(t, response.Error)
	result := response.Result.(nipcash.CashStatusResult)
	require.Len(t, result.Recipients, 1)
	assert.Equal(t, db.CashIdentityCash, result.Recipients[0].IdentityType)
	assert.Empty(t, result.Recipients[0].IdentityValue, "a cash row carries no identity to disclose")
}
