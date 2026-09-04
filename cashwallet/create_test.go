package cashwallet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

func newTestDeps(svc *tests.TestService) Deps {
	return Deps{
		AppsService:         svc.AppsService,
		TransactionsService: transactions.NewTransactionsService(svc.DB, svc.EventPublisher),
		LNClient:            svc.LNClient,
		Keys:                svc.Keys,
		DB:                  svc.DB,
		RelayURLs:           []string{"wss://relay.test"},
		IAChecker:           apps.NewIdentityAuthorityManager(svc.DB),
	}
}

// registerTrustedIA registers iaPubkey as a trusted Identity Authority on svc's DB.
func registerTrustedIA(t *testing.T, svc *tests.TestService, iaPubkey string) {
	t.Helper()
	_, err := apps.NewIdentityAuthorityManager(svc.DB).Add(iaPubkey, "test-ia", nil)
	require.NoError(t, err)
}

func onePubkeyRecipient(amountMloki uint64) []RecipientInput {
	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	return []RecipientInput{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: amountMloki},
	}
}

func oneBearerRecipient(amountMloki uint64) []RecipientInput {
	return []RecipientInput{
		{IdentityType: db.CashIdentityBearer, AmountMloki: amountMloki},
	}
}

func TestCreate_SingleRecipient_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		ExpirySecs: 1800,
	})
	require.NoError(t, err)
	require.NotNil(t, result.WalletApp)
	assert.Contains(t, result.PairingURI, "nostr+walletconnect://")
	assert.Contains(t, result.PairingURI, "?relay=wss://relay.test")
	require.Len(t, result.Recipients, 1)
	assert.Equal(t, uint64(1000), result.Recipients[0].AmountMloki)
	require.NotNil(t, result.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(1800*time.Second), *result.ExpiresAt, 5*time.Second)

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps)
	assert.Len(t, childApps, 1)
	assert.Equal(t, db.ParentKindCash, childApps[0].ParentKind)

	// Hardened scope surface: exactly cash_redeem + cash_transfer +
	// get_balance, never pay_invoice/lookup_invoice/list_transactions.
	var perms []db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ?", childApps[0].ID).Find(&perms).Error)
	scopes := make([]string, len(perms))
	for i, p := range perms {
		scopes[i] = p.Scope
	}
	assert.ElementsMatch(t, []string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.CASH_CONSOLIDATE_SCOPE, constants.GET_BALANCE_SCOPE}, scopes)

	var claims []db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", childApps[0].ID).Find(&claims).Error)
	require.Len(t, claims, 1)
	assert.Equal(t, int64(1000), claims[0].AmountMloki)
	assert.Nil(t, claims[0].ClaimedAt)
}

// TestCreate_CashTokenMatchesPairingURI is the fund-safety property that
// matters most for the lokicash token: it must decode to *exactly* the same
// wallet pubkey, secret, and relay set as PairingURI, since either string
// alone is a fully sufficient connection credential (NIP-JW §The Lokicash
// Token). If the two ever diverged, a recipient using one but not the other
// could end up connected to a different wallet than intended.
func TestCreate_CashTokenMatchesPairingURI(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		ExpirySecs: 1800,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.CashToken)
	assert.True(t, strings.HasPrefix(result.CashToken, lokicash.HRP+"1"))

	pairingURI, err := url.Parse(result.PairingURI)
	require.NoError(t, err)
	wantPubkey := pairingURI.Host
	wantSecret := pairingURI.Query().Get("secret")
	wantRelays := pairingURI.Query()["relay"]

	decoded, err := lokicash.Decode(result.CashToken)
	require.NoError(t, err)
	assert.Equal(t, lokicash.HRP, decoded.HRP)
	assert.Equal(t, wantPubkey, decoded.WalletPubkey)
	assert.Equal(t, wantSecret, decoded.Secret)
	assert.Equal(t, wantRelays, decoded.RelayURLs)
}

// TestCreate_CashTokenIdentityRequiredHint verifies Commit populates the
// lokicash token's IdentityRequired hint correctly for both a plain
// identity-bound wallet and a solo bearer one. Split into two independent
// services (rather than two Create calls against one): the mock LN client's
// payment-hash idempotency is instance-wide, and both calls would otherwise
// reuse the same default mock invoice, making the second spuriously fail as
// "already paid".
func TestCreate_CashTokenIdentityRequiredHint(t *testing.T) {
	t.Run("identity-bound", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()

		hub := tests.CreateCashHub(t, svc, 100_000, 3600)
		tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

		result, err := Create(context.TODO(), newTestDeps(svc), Params{
			HubApp:     hub,
			Recipients: onePubkeyRecipient(1000),
			ExpirySecs: 1800,
		})
		require.NoError(t, err)
		decoded, err := lokicash.Decode(result.CashToken)
		require.NoError(t, err)
		require.NotNil(t, decoded.IdentityRequired)
		assert.True(t, *decoded.IdentityRequired)
	})

	t.Run("bearer", func(t *testing.T) {
		svc, err := tests.CreateTestService(t)
		require.NoError(t, err)
		defer svc.Remove()

		hub := tests.CreateCashHub(t, svc, 100_000, 3600)
		tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

		result, err := Create(context.TODO(), newTestDeps(svc), Params{
			HubApp:     hub,
			Recipients: oneBearerRecipient(1000),
			ExpirySecs: 1800,
		})
		require.NoError(t, err)
		decoded, err := lokicash.Decode(result.CashToken)
		require.NoError(t, err)
		require.NotNil(t, decoded.IdentityRequired)
		assert.False(t, *decoded.IdentityRequired)
	})
}

// TestSplit_BearerTarget_CashTokenHints verifies Split's new wallet
// reports IdentityRequired: false for a bearer target — a split-off bearer
// wallet is always a single bearer slice, by construction.
func TestSplit_BearerTarget_CashTokenHints(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	sourceWallet, _, err := svc.AppsService.CreateApp(
		"source-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, sourceWallet.ID, 200_000, "sourcefundtxhash")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-split-test", Amount: 1000},
	}

	commitment := strings.Repeat("ef", 32)
	splitExpiresAt := time.Now().Add(time.Hour)
	result, err := Split(context.TODO(), newTestDeps(svc), SplitParams{
		HubApp:           hub,
		SourceWalletApp:  sourceWallet,
		AmountMloki:      1000,
		NewIdentityType:  db.CashIdentityBearer,
		NewIdentityValue: commitment,
		ExpiresAt:        &splitExpiresAt,
	})
	require.NoError(t, err)

	decoded, err := lokicash.Decode(result.CashToken)
	require.NoError(t, err)
	require.NotNil(t, decoded.IdentityRequired)
	assert.False(t, *decoded.IdentityRequired)
}

// TestSplit_PubkeyTarget_CashTokenHints verifies Split can mint a
// dedicated wallet for a non-bearer target too — the unified transfer model
// spins off for every identity type once a wallet's recipient history rules
// out a cheap in-place reassignment, not only when converting into bearer.
func TestSplit_PubkeyTarget_CashTokenHints(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	sourceWallet, _, err := svc.AppsService.CreateApp(
		"source-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, sourceWallet.ID, 200_000, "sourcefundtxhash2")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-split-pubkey-test", Amount: 1000},
	}

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	splitExpiresAt := time.Now().Add(time.Hour)
	result, err := Split(context.TODO(), newTestDeps(svc), SplitParams{
		HubApp:           hub,
		SourceWalletApp:  sourceWallet,
		AmountMloki:      1000,
		NewIdentityType:  db.CashIdentityPubkey,
		NewIdentityValue: newPubkey,
		MinTransferMloki: 100,
		ExpiresAt:        &splitExpiresAt,
	})
	require.NoError(t, err)

	decoded, err := lokicash.Decode(result.CashToken)
	require.NoError(t, err)
	require.NotNil(t, decoded.IdentityRequired)
	assert.True(t, *decoded.IdentityRequired, "a pubkey-mode split target requires identity proof to redeem/transfer")

	var claim db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", result.WalletApp.ID).First(&claim).Error)
	assert.Equal(t, db.CashIdentityPubkey, claim.IdentityType)
	assert.Equal(t, newPubkey, claim.IdentityValue)
	assert.Equal(t, int64(100), claim.MinTransferMloki, "the new wallet must inherit MinTransferMloki from SplitParams")
}

// TestSplit_SourceNeverExpires_NewWalletNeverExpires verifies a split off a
// source wallet that never expires (ExpiresAt nil, e.g. minted under a Cash
// Hub configured with MaxExpSecs == 0) produces a new dedicated wallet that
// also never expires — a split relocates an existing entitlement, including
// its "never" status, it does not shorten it (NIP-CASH "Spinning a Slice Off
// Into a Dedicated Wallet" -> Eligibility and limits).
func TestSplit_SourceNeverExpires_NewWalletNeverExpires(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 0) // MaxExpSecs = 0 = never
	sourceWallet, _, err := svc.AppsService.CreateApp(
		"source-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil, // nil expiresAt = never
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	require.Nil(t, sourceWallet.ExpiresAt)
	tests.FundApp(svc, sourceWallet.ID, 200_000, "sourcefundtxhash3")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-split-never-test", Amount: 1000},
	}

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	result, err := Split(context.TODO(), newTestDeps(svc), SplitParams{
		HubApp:           hub,
		SourceWalletApp:  sourceWallet,
		AmountMloki:      1000,
		NewIdentityType:  db.CashIdentityPubkey,
		NewIdentityValue: newPubkey,
		ExpiresAt:        sourceWallet.ExpiresAt, // nil — mirrors cash_transfer_controller.go's inheritance
	})
	require.NoError(t, err)
	assert.Nil(t, result.WalletApp.ExpiresAt, "a split off a never-expiring source must also never expire")
}

func TestCreate_MultipleRecipients_OneSharedWallet_CustomAmounts_SharedExpiry(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pk2, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	connKey := tests.RandomHex32()
	iaPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	registerTrustedIA(t, svc, iaPubkey)

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pk1, AmountMloki: 1000},
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pk2, AmountMloki: 2500},
			{IdentityType: db.CashIdentityConnectionKey, IdentityValue: connKey, IAPubkey: iaPubkey, AmountMloki: 500},
		},
		ExpirySecs: 900,
	})
	require.NoError(t, err)

	// Exactly one shared wallet app, not three.
	var childApps []db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps).Error)
	require.Len(t, childApps, 1)
	assert.Equal(t, result.WalletApp.ID, childApps[0].ID)
	// MaxAmountLoki funded is the SUM (1000+2500+500 = 4000 mloki = 4 loki).
	var perm db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", childApps[0].ID, constants.CASH_REDEEM_SCOPE).First(&perm).Error)
	assert.Equal(t, 4, perm.MaxAmountLoki)

	// Three independent claim rows, each with its own amount, all sharing one expiry.
	var claims []db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", childApps[0].ID).Find(&claims).Error)
	require.Len(t, claims, 3)
	byIdentity := map[string]db.CashWalletClaim{}
	for _, c := range claims {
		byIdentity[c.IdentityValue] = c
	}
	assert.Equal(t, int64(1000), byIdentity[pk1].AmountMloki)
	assert.Equal(t, int64(2500), byIdentity[pk2].AmountMloki)
	assert.Equal(t, int64(500), byIdentity[connKey].AmountMloki)
	assert.Equal(t, iaPubkey, byIdentity[connKey].IAPubkey)

	require.NotNil(t, childApps[0].ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(900*time.Second), *childApps[0].ExpiresAt, 5*time.Second)

	require.Len(t, result.Recipients, 3)
}

func TestCreate_EmptyRecipients_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)

	_, err = Create(context.TODO(), newTestDeps(svc), Params{HubApp: hub, Recipients: []RecipientInput{}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_TooManyRecipients_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000_000, 3600)
	tests.FundApp(svc, hub.ID, 100_000_000, "fundtxhash")

	recipients := make([]RecipientInput, maxRecipientsPerWallet+1)
	for i := range recipients {
		pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
		recipients[i] = RecipientInput{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 1}
	}

	_, err = Create(context.TODO(), newTestDeps(svc), Params{HubApp: hub, Recipients: recipients})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_DuplicateIdentityInBatch_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 500},
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pk, AmountMloki: 500},
		},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_ConnectionKeyMode_MissingIAPubkey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.CashIdentityConnectionKey, IdentityValue: tests.RandomHex32(), AmountMloki: 1000},
		},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_ConnectionKeyMode_InvalidHex(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	iaPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.CashIdentityConnectionKey, IdentityValue: "not-valid-hex!", IAPubkey: iaPubkey, AmountMloki: 1000},
		},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_ConnectionKeyMode_UntrustedIARejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	iaPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	// Deliberately not registered via registerTrustedIA.

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.CashIdentityConnectionKey, IdentityValue: tests.RandomHex32(), IAPubkey: iaPubkey, AmountMloki: 1000},
		},
		ExpirySecs: 1800,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
	assert.Contains(t, err.Error(), "not a trusted Identity Authority")
}

func TestCreate_NotCashHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     isolatedApp,
		Recipients: onePubkeyRecipient(1000),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_InsufficientBalance_ForSumOfAllRecipients(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	// hub has 0 balance — do NOT fund it.

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(5000),
		ExpirySecs: 1800,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, transactions.NewInsufficientBalanceError()))
}

func TestCreate_SumOfRecipients_ExceedsPerWalletMaxTotal_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 5000, 3600) // per-wallet (total) max 5000 mloki
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pk2, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	// Each recipient individually under the cap, but the sum (3000+3000=6000) exceeds it.
	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pk1, AmountMloki: 3000},
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pk2, AmountMloki: 3000},
		},
		ExpirySecs: 1800,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, transactions.NewQuotaExceededError()))
}

// TestCreate_RecipientSumOverflow_Rejected reproduces a bypass of the hub's
// PerWalletMaxMloki cap: two recipients each individually below MaxInt64
// (so a single-value guard wouldn't catch them) whose uint64 sum wraps
// around to a small number, which would otherwise pass both the
// PerWalletMaxMloki comparison and the hub-balance pre-flight check while
// leaving each recipient's own stored entitlement at its original,
// un-wrapped (and therefore uncollectable — the wallet is never actually
// funded that much) amount.
func TestCreate_RecipientSumOverflow_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600) // per-wallet (total) max 1,000,000 mloki
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pk2, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pk1, AmountMloki: math.MaxUint64 - 500},
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pk2, AmountMloki: 1000},
		},
		ExpirySecs: 1800,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

// TestCreate_RecipientSumExceedsInt64_Rejected is the regression test for a
// narrower gap the uint64-wraparound guard above didn't cover: two
// recipients each individually under MaxInt64 (so neither the single-value
// guard nor a uint64-wraparound check fires) whose sum still exceeds
// MaxInt64 - the exact range int64(sum) > balance further down casts into.
// Pre-fix this sum would silently wrap to a negative int64, comparing as
// less than any real balance and passing the pre-flight check regardless of
// the hub's actual balance.
//
// PerWalletMaxMloki is forced to 0 directly via the DB (CreateCashHub's own
// API validation rejects 0, so this simulates a pre-validation/legacy row)
// so the PerWalletMaxMloki cap check - which, since a cap is itself
// int-bounded, would otherwise always independently catch any sum this
// large anyway - doesn't mask whether this guard specifically works.
func TestCreate_RecipientSumExceedsInt64_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 1, 3600)
	require.NoError(t, svc.DB.Model(&db.CashHubConfig{}).Where("app_id = ?", hub.ID).Update("per_wallet_max_mloki", 0).Error)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pk2, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pk1, AmountMloki: math.MaxInt64 - 500},
			{IdentityType: db.CashIdentityPubkey, IdentityValue: pk2, AmountMloki: 1000},
		},
		ExpirySecs: 1800,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_ExpiryExceedsMax(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600) // max 3600 secs

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		ExpirySecs: 7200,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_OmittedExpiry_DefaultsToHubMax(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		// ExpirySecs omitted (zero value).
	})
	require.NoError(t, err)
	require.NotNil(t, result.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(3600*time.Second), *result.ExpiresAt, 5*time.Second,
		"wallet must default to the hub's max_exp_secs, not expire immediately")
}

// TestCreate_HubNeverExpires_OmittedCallExpiry_NeverExpires covers a Cash Hub
// configured with MaxExpSecs == 0 ("never" — no ceiling on how long an
// issued wallet may remain unredeemed). Omitting the per-call expiry must
// resolve to a wallet that never expires (nil ExpiresAt), not to the
// zero-duration/already-expired bug this same code path used to guard
// against before "never" was a legitimate hub setting.
func TestCreate_HubNeverExpires_OmittedCallExpiry_NeverExpires(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 0) // MaxExpSecs = 0 = never
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		// ExpirySecs omitted (zero value).
	})
	require.NoError(t, err)
	assert.Nil(t, result.ExpiresAt, "hub has no ceiling and the caller requested none, so the wallet must never expire")
	assert.Nil(t, result.WalletApp.ExpiresAt, "the persisted App row must carry the same nil expiry")
}

// TestCreate_HubNeverExpires_ExplicitCallExpiry_Honored covers the same
// never-ceiling hub, but a caller that DOES request its own, finite expiry —
// there's no hub ceiling to cap it against, so it must be honored exactly.
func TestCreate_HubNeverExpires_ExplicitCallExpiry_Honored(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 0) // MaxExpSecs = 0 = never
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		ExpirySecs: 900,
	})
	require.NoError(t, err)
	require.NotNil(t, result.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(900*time.Second), *result.ExpiresAt, 5*time.Second)
}

// TestCreate_HubNeverExpires_ExplicitCallExpiry_TooLarge_Rejected verifies a
// never-ceiling hub still rejects a per-call expiry large enough to overflow
// time.Duration's nanosecond range, rather than silently wrapping into a
// bogus (possibly already-past) ExpiresAt.
func TestCreate_HubNeverExpires_ExplicitCallExpiry_TooLarge_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 0) // MaxExpSecs = 0 = never
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		ExpirySecs: constants.MAX_EXPIRY_SECS + 1,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_TransferFailure_Rollback(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.PayInvoiceResponses = []*lnclient.PayInvoiceResponse{nil}
	mockLN.PayInvoiceErrors = []error{errors.New("simulated payment failure")}

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		ExpirySecs: 1800,
	})
	require.Error(t, err)

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps)
	assert.Empty(t, childApps, "the child Cash wallet app must be rolled back after a funding failure")

	var claims []db.CashWalletClaim
	svc.DB.Find(&claims)
	assert.Empty(t, claims, "claim rows must be rolled back too (FK cascade on the deleted app)")
}

// TestCreate_ConcurrentCreation_BothIndependentlySucceed verifies two
// concurrent Create calls for two different recipient sets against the same
// hub don't interfere with each other — each produces its own independent
// wallet, funded correctly from the shared hub balance.
func TestCreate_ConcurrentCreation_BothIndependentlySucceed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-a", Amount: 1000},
		{Type: "incoming", Invoice: tests.MockZeroAmountInvoice, PaymentHash: tests.MockZeroAmountPaymentHash, Preimage: "preimage-b", Amount: 1000},
	}

	var wg sync.WaitGroup
	results := make([]*Result, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = Create(context.TODO(), newTestDeps(svc), Params{
				HubApp:     hub,
				Recipients: onePubkeyRecipient(1000),
				ExpirySecs: 1800,
			})
		}(i)
	}
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.NotEqual(t, results[0].WalletApp.ID, results[1].WalletApp.ID)

	var childApps []db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps).Error)
	assert.Len(t, childApps, 2)
	_ = childApps
}

func TestCreate_Bearer_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: oneBearerRecipient(1000),
		ExpirySecs: 1800,
	})
	require.NoError(t, err)
	require.Len(t, result.Recipients, 1)

	r := result.Recipients[0]
	assert.Equal(t, db.CashIdentityBearer, r.IdentityType)
	assert.Equal(t, uint64(1000), r.AmountMloki)
	assert.NotEmpty(t, r.BearerSecret, "the plaintext secret must be returned exactly once")
	assert.Empty(t, r.IdentityValue, "the response must never surface the internal secret hash as identity_value")

	var childApps []db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps).Error)
	require.Len(t, childApps, 1)

	var claims []db.CashWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", childApps[0].ID).Find(&claims).Error)
	require.Len(t, claims, 1)
	assert.Equal(t, db.CashIdentityBearer, claims[0].IdentityType)
	assert.Equal(t, int64(1000), claims[0].AmountMloki)

	// The critical fund-safety property: the DB row must never hold the raw
	// secret, only its hash. A read of this table (backup leak, SQL
	// injection, a careless log line) must not by itself be enough to steal
	// every unclaimed bearer slice on the instance.
	assert.NotEqual(t, r.BearerSecret, claims[0].IdentityValue,
		"the stored identity_value must be a hash of the secret, never the secret itself")
	rawSecret, hexErr := hex.DecodeString(r.BearerSecret)
	require.NoError(t, hexErr)
	wantHash := sha256.Sum256(rawSecret)
	assert.Equal(t, hex.EncodeToString(wantHash[:]), claims[0].IdentityValue,
		"stored identity_value must be exactly sha256(secret)")
}

func TestCreate_Bearer_SecretsAreUnique(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	// The mock LN client returns a fixed invoice/payment_hash regardless of
	// amount unless explicitly queued — two Create calls in the same test
	// need distinct queued invoices, or the second payment fails as an
	// already-paid replay. Same workaround as the concurrent-creation test
	// below.
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-a", Amount: 1000},
		{Type: "incoming", Invoice: tests.MockZeroAmountInvoice, PaymentHash: tests.MockZeroAmountPaymentHash, Preimage: "preimage-b", Amount: 1000},
	}

	result1, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub, Recipients: oneBearerRecipient(1000), ExpirySecs: 1800,
	})
	require.NoError(t, err)
	result2, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub, Recipients: oneBearerRecipient(1000), ExpirySecs: 1800,
	})
	require.NoError(t, err)

	assert.NotEqual(t, result1.Recipients[0].BearerSecret, result2.Recipients[0].BearerSecret)
}

func TestCreate_Bearer_RejectsMixedRecipients(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pubkeyRecipient := onePubkeyRecipient(500)[0]
	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			pubkeyRecipient,
			{IdentityType: db.CashIdentityBearer, AmountMloki: 500},
		},
		ExpirySecs: 1800,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)

	var childApps []db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindCashWallet).Find(&childApps).Error)
	assert.Empty(t, childApps, "a rejected request must leave no partial wallet behind")
}

func TestCreate_Bearer_RejectsTwoBearerRecipients(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.CashIdentityBearer, AmountMloki: 500},
			{IdentityType: db.CashIdentityBearer, AmountMloki: 500},
		},
		ExpirySecs: 1800,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestCreate_Bearer_RejectsCallerSuppliedIdentityValue(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.CashIdentityBearer, IdentityValue: tests.RandomHex32(), AmountMloki: 500},
		},
		ExpirySecs: 1800,
	})
	require.Error(t, err, "the caller has no way to prove the entropy of a self-supplied secret; only the Hub may generate one")
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}

func TestCreate_Bearer_RejectsCallerSuppliedIAPubkey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.CashIdentityBearer, IAPubkey: tests.RandomHex32(), AmountMloki: 500},
		},
		ExpirySecs: 1800,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidParams)
}
