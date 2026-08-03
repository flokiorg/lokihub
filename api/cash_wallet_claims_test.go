package api

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/tests"
)

// selfPaymentPubkey is the destination pubkey embedded in tests.MockInvoice —
// setting the mock LN client's pubkey to this makes an internal transfer's
// incoming leg settle synchronously (recognised as a self-payment), matching
// service/cash_cleanup_service_test.go's setup for the same reason.
const selfPaymentPubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"

// newBareCashWallet creates a cash_wallet child directly (bypassing
// cashwallet.Create), for tests that only need a wallet + claim rows to exist,
// not a real funding transfer.
func newBareCashWallet(t *testing.T, svc *tests.TestService, hub *db.App, maxAmountLoki uint64) *db.App {
	t.Helper()
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-child", "", maxAmountLoki, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	return wallet
}

// --- ListCashWalletClaims ---

func TestListCashWalletClaims_EmptyHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	theAPI := newTestAPI(svc)

	result, _, _, err := theAPI.ListCashWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestListCashWalletClaims_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 10)
	pk1 := tests.RandomHex32()
	pk2 := tests.RandomHex32()

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk1, AmountMloki: 3000},
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: pk2, AmountMloki: 7000},
	}))

	theAPI := newTestAPI(svc)
	result, _, _, err := theAPI.ListCashWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	require.Len(t, result, 2)

	ids := map[string]bool{}
	for _, r := range result {
		ids[r.IdentityValue] = true
		assert.False(t, r.Claimed)
		assert.Nil(t, r.ClaimedAt)
		assert.Equal(t, wallet.ID, r.WalletAppID)
		assert.Greater(t, r.CreatedAt, int64(0))
	}
	assert.True(t, ids[pk1])
	assert.True(t, ids[pk2])
}

// TestListCashWalletClaims_CashTokenPerWallet verifies every claim
// belonging to the same wallet gets the identical, correctly-derived
// lokicash token, and that a different wallet gets a different one — the
// UI groups claims by wallet_app_id and reads the token off any one claim
// in the group, so a mismatch here would show the wrong connection.
func TestListCashWalletClaims_CashTokenPerWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	walletA := newBareCashWallet(t, svc, hub, 10)
	walletB := newBareCashWallet(t, svc, hub, 5)
	pk1 := tests.RandomHex32()
	pk2 := tests.RandomHex32()
	pk3 := tests.RandomHex32()

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(walletA.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk1, AmountMloki: 3000},
		{IdentityType: db.CashIdentityConnectionKey, IdentityValue: pk2, AmountMloki: 7000},
	}))
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(walletB.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk3, AmountMloki: 5000},
	}))

	theAPI := newTestAPI(svc)
	result, _, _, err := theAPI.ListCashWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	require.Len(t, result, 3)

	tokenByWallet := map[uint]string{}
	for _, r := range result {
		require.NotEmpty(t, r.CashToken, "wallet %d claim missing a lokicash token", r.WalletAppID)
		if existing, ok := tokenByWallet[r.WalletAppID]; ok {
			assert.Equal(t, existing, r.CashToken, "claims sharing a wallet must get the identical token")
		}
		tokenByWallet[r.WalletAppID] = r.CashToken
	}
	require.Len(t, tokenByWallet, 2)
	assert.NotEqual(t, tokenByWallet[walletA.ID], tokenByWallet[walletB.ID])

	// The listed token must be the SAME connection GetCashWalletConnection
	// would derive for that wallet directly — not just present, but correct.
	conn, err := theAPI.GetCashWalletConnection(walletA.ID)
	require.NoError(t, err)
	assert.Equal(t, conn.CashToken, tokenByWallet[walletA.ID])

	decoded, err := lokicash.Decode(tokenByWallet[walletA.ID])
	require.NoError(t, err)
	require.NotNil(t, walletA.WalletPubkey)
	assert.Equal(t, *walletA.WalletPubkey, decoded.WalletPubkey)
}

func TestListCashWalletClaims_ShowsClaimedStatus(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 3)
	pk := tests.RandomHex32()

	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 3000},
	}))
	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, pk)
	require.NoError(t, err)

	theAPI := newTestAPI(svc)
	result, _, _, err := theAPI.ListCashWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.True(t, result[0].Claimed)
	assert.NotNil(t, result[0].ClaimedAt)
}

func TestListCashWalletClaims_StatusAndCounts(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	theAPI := newTestAPI(svc)

	// Unclaimed slice.
	unclaimedWallet := newBareCashWallet(t, svc, hub, 2)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(unclaimedWallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), AmountMloki: 2000},
	}))

	// Claimed slice.
	claimedWallet := newBareCashWallet(t, svc, hub, 3)
	claimedPk := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(claimedWallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimedPk, AmountMloki: 3000},
	}))
	_, err = svc.AppsService.ClaimCashSlice(claimedWallet.ID, db.CashIdentityPubkey, claimedPk)
	require.NoError(t, err)

	all, totalCount, counts, err := theAPI.ListCashWalletClaims(hub.ID, 0, 0, "")
	require.NoError(t, err)
	assert.Len(t, all, 2)
	assert.EqualValues(t, 2, totalCount)
	assert.EqualValues(t, 2, counts.All)
	assert.EqualValues(t, 1, counts.Unclaimed)
	assert.EqualValues(t, 1, counts.Claimed)

	unclaimedOnly, _, _, err := theAPI.ListCashWalletClaims(hub.ID, 0, 0, CashAllocationStatusUnclaimed)
	require.NoError(t, err)
	assert.Len(t, unclaimedOnly, 1)

	claimedOnly, _, _, err := theAPI.ListCashWalletClaims(hub.ID, 0, 0, CashAllocationStatusClaimed)
	require.NoError(t, err)
	assert.Len(t, claimedOnly, 1)
}

func TestListCashWalletClaims_NotCashHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPI(svc)
	_, _, _, err = theAPI.ListCashWalletClaims(isolatedApp.ID, 0, 0, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a cash_hub")
}

// --- DeleteCashClaim ---

func TestDeleteCashClaim_Unclaimed_SweepsBackToHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = selfPaymentPubkey
	// Two distinct canned invoices, not one: this claim is the wallet's only
	// one, so api.DeleteCashClaim's cascade now also reclaims/deletes
	// the now-empty wallet (see its own doc comment) - a second internal
	// transfer, on top of the sweep-back this test already exercised. Reusing
	// the same default canned invoice for both would make the second
	// SendPaymentSync collide with the first's already-SETTLED payment_hash
	// ("this invoice has already been paid"), which is a mock-fixture
	// limitation (MockLn.MakeInvoice ignores amount/description and always
	// returns the same canned transaction unless queued), not anything a
	// real LN client would ever do for two genuinely separate invoices - see
	// cashwallet/create_test.go's TestCreate_ConcurrentCreation_.. for the
	// same two-distinct-invoices pattern.
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-a", Amount: 1000},
		{Type: "incoming", Invoice: tests.MockZeroAmountInvoice, PaymentHash: tests.MockZeroAmountPaymentHash, Preimage: "preimage-b", Amount: 1000},
	}

	hub := tests.CreateCashHub(t, svc, 300_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 200)
	pk := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 200_000},
	}))
	tests.FundApp(svc, wallet.ID, 200_000, "fund-wallet")

	var claim db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", wallet.ID).First(&claim).Error)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteCashClaim(hub.ID, wallet.ID, claim.ID)
	require.NoError(t, err)

	var count int64
	svc.DB.Model(&db.CashWalletClaim{}).Where("id = ?", claim.ID).Count(&count)
	assert.Zero(t, count, "the claim row must be deleted")

	var walletCount int64
	svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Count(&walletCount)
	assert.Zero(t, walletCount, "removing the wallet's last remaining claim must also reclaim/delete the now-empty wallet itself")
}

// TestDeleteCashClaim_OneOfMultiple_WalletSurvives is the boundary case
// TestDeleteCashClaim_Unclaimed_SweepsBackToHub's cascade doesn't
// apply to: removing one recipient's slice from an otherwise-live shared
// wallet must leave the wallet itself (and its other recipient's slice)
// alone - only the wallet's *last* claim triggers the cascade delete.
func TestDeleteCashClaim_OneOfMultiple_WalletSurvives(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).Pubkey = selfPaymentPubkey

	hub := tests.CreateCashHub(t, svc, 300_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 200)
	pkRemoved := tests.RandomHex32()
	pkRemaining := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pkRemoved, AmountMloki: 100_000},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pkRemaining, AmountMloki: 100_000},
	}))
	tests.FundApp(svc, wallet.ID, 200_000, "fund-wallet")

	var claimToRemove db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, pkRemoved).First(&claimToRemove).Error)

	theAPI := newTestAPIWithService(t, svc)
	require.NoError(t, theAPI.DeleteCashClaim(hub.ID, wallet.ID, claimToRemove.ID))

	var removedCount int64
	svc.DB.Model(&db.CashWalletClaim{}).Where("id = ?", claimToRemove.ID).Count(&removedCount)
	assert.Zero(t, removedCount, "the removed claim row must be gone")

	var remainingCount int64
	svc.DB.Model(&db.CashWalletClaim{}).Where("wallet_app_id = ? AND identity_value = ?", wallet.ID, pkRemaining).Count(&remainingCount)
	assert.EqualValues(t, 1, remainingCount, "the other recipient's slice must be untouched")

	var walletCount int64
	svc.DB.Model(&db.App{}).Where("id = ?", wallet.ID).Count(&walletCount)
	assert.EqualValues(t, 1, walletCount, "the wallet itself must survive while it still has a live recipient")
}

func TestDeleteCashClaim_AlreadyClaimed_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 3)
	pk := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 3000},
	}))
	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, pk)
	require.NoError(t, err)

	var claim db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", wallet.ID).First(&claim).Error)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteCashClaim(hub.ID, wallet.ID, claim.ID)
	require.Error(t, err, "deleting an already-claimed slice must be rejected")

	var count int64
	svc.DB.Model(&db.CashWalletClaim{}).Where("id = ?", claim.ID).Count(&count)
	assert.EqualValues(t, 1, count, "the claim row must be untouched")
}

func TestDeleteCashClaim_NotCashHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteCashClaim(isolatedApp.ID, 1, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a cash_hub")
}

func TestDeleteCashClaim_NotCashWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPIWithService(t, svc)
	err = theAPI.DeleteCashClaim(hub.ID, isolatedApp.ID, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cash wallet not found for this hub")
}

// TestDeleteCashClaim_WrongHub_Rejected is the regression test for the
// cross-hub deletion bug found in code review: api.DeleteCashClaim used
// to take only (walletAppID, claimID), with no check that walletAppID
// actually belonged to the hub named in the request's URL - so a caller
// scoped to hub A could delete (and redirect the sweep-back of) a claim that
// actually belonged to a completely unrelated hub B, simply by supplying
// hub B's walletId/claimId while hitting hub A's endpoint. hubAppID is now a
// required parameter, checked the same way DeleteCashWallet already checks it.
func TestDeleteCashClaim_WrongHub_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hubA := tests.CreateCashHub(t, svc, 10_000, 3600)
	hubB := tests.CreateCashHub(t, svc, 10_000, 3600)
	walletB := newBareCashWallet(t, svc, hubB, 3)
	pk := tests.RandomHex32()
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(walletB.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 3000},
	}))

	var claim db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", walletB.ID).First(&claim).Error)

	theAPI := newTestAPIWithService(t, svc)
	// hubA's URL, but walletB/claim actually belong to hubB.
	err = theAPI.DeleteCashClaim(hubA.ID, walletB.ID, claim.ID)
	require.Error(t, err, "a claim belonging to a different hub must not be deletable through this hub's endpoint")
	assert.Contains(t, err.Error(), "not found for this hub")

	var count int64
	svc.DB.Model(&db.CashWalletClaim{}).Where("id = ?", claim.ID).Count(&count)
	assert.EqualValues(t, 1, count, "the claim must be untouched")
}

// --- GetCashWalletConnection ---

func TestGetCashWalletConnection_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 0)

	theAPI := newTestAPI(svc)
	conn, err := theAPI.GetCashWalletConnection(wallet.ID)
	require.NoError(t, err)
	assert.Contains(t, conn.PairingURI, "nostr+walletconnect://")
	assert.Contains(t, conn.PairingURI, "&secret=")
	assert.True(t, strings.HasPrefix(conn.CashToken, lokicash.HRP+"1"))

	// Deterministic: re-deriving must return the exact same URI and token
	// every time — a stale lokicash1... token handed out earlier must still
	// resolve to the same wallet as a freshly re-derived one.
	again, err := theAPI.GetCashWalletConnection(wallet.ID)
	require.NoError(t, err)
	assert.Equal(t, conn.PairingURI, again.PairingURI)
	assert.Equal(t, conn.CashToken, again.CashToken)

	// And the two must actually agree with each other, not just each be
	// internally deterministic.
	pairingURI, err := url.Parse(conn.PairingURI)
	require.NoError(t, err)
	decoded, err := lokicash.Decode(conn.CashToken)
	require.NoError(t, err)
	assert.Equal(t, pairingURI.Host, decoded.WalletPubkey)
	assert.Equal(t, pairingURI.Query().Get("secret"), decoded.Secret)
	assert.Equal(t, pairingURI.Query()["relay"], decoded.RelayURLs)
}

// TestGetCashWalletConnection_LokicashHintsReflectCurrentClaims verifies the
// lokicash token's identity-required hint is re-derived from the wallet's
// CURRENT claim rows on every call, not cached from creation — necessary
// because a solo wallet's sole recipient can move into or out of bearer
// status via cash_transfer well after the wallet (and its first-ever token)
// was created.
func TestGetCashWalletConnection_LokicashHintsReflectCurrentClaims(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 0)
	pubkey := strings.Repeat("ab", 32)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pubkey, AmountMloki: 1000},
	}))

	theAPI := newTestAPI(svc)
	conn, err := theAPI.GetCashWalletConnection(wallet.ID)
	require.NoError(t, err)
	decoded, err := lokicash.Decode(conn.CashToken)
	require.NoError(t, err)
	require.NotNil(t, decoded.IdentityRequired)
	assert.True(t, *decoded.IdentityRequired)

	// Flip the sole recipient into bearer status via ReassignCashSliceIdentity
	// directly (the DB-service layer cash_transfer itself calls) — re-deriving
	// the connection afterward must reflect the change, not the stale
	// creation-time value.
	_, err = svc.AppsService.ReassignCashSliceIdentity(wallet.ID,
		db.CashIdentityPubkey, pubkey, db.CashIdentityBearer, strings.Repeat("cd", 32), "")
	require.NoError(t, err)

	connAfter, err := theAPI.GetCashWalletConnection(wallet.ID)
	require.NoError(t, err)
	decodedAfter, err := lokicash.Decode(connAfter.CashToken)
	require.NoError(t, err)
	require.NotNil(t, decodedAfter.IdentityRequired)
	assert.False(t, *decodedAfter.IdentityRequired, "the token must reflect the wallet's current bearer status, not its status at creation")
}

// TestGetCashWalletConnection_NoClaims_HintsOmitted verifies a wallet with no
// claim rows left (e.g. every recipient individually removed) produces a
// token with neither hint set, rather than guessing.
func TestGetCashWalletConnection_NoClaims_HintsOmitted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 10_000, 3600)
	wallet := newBareCashWallet(t, svc, hub, 0)

	theAPI := newTestAPI(svc)
	conn, err := theAPI.GetCashWalletConnection(wallet.ID)
	require.NoError(t, err)
	decoded, err := lokicash.Decode(conn.CashToken)
	require.NoError(t, err)
	assert.Nil(t, decoded.IdentityRequired)
}

func TestGetCashWalletConnection_NotCashWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	theAPI := newTestAPI(svc)
	_, err = theAPI.GetCashWalletConnection(isolatedApp.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a cash_wallet")
}
