package jitwallet

import (
	"context"
	"errors"
	"math"
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
		{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk, AmountMloki: amountMloki},
	}
}

func TestCreate_SingleRecipient_HappyPath(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
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
	assert.WithinDuration(t, time.Now().Add(1800*time.Second), result.ExpiresAt, 5*time.Second)

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps)
	assert.Len(t, childApps, 1)
	assert.Equal(t, db.ParentKindJIT, childApps[0].ParentKind)

	// Hardened scope surface: exactly jit_claim_funds + get_balance, never
	// pay_invoice/lookup_invoice/list_transactions.
	var perms []db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ?", childApps[0].ID).Find(&perms).Error)
	scopes := make([]string, len(perms))
	for i, p := range perms {
		scopes[i] = p.Scope
	}
	assert.ElementsMatch(t, []string{constants.JIT_CLAIM_FUNDS_SCOPE, constants.GET_BALANCE_SCOPE}, scopes)

	var claims []db.JITWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", childApps[0].ID).Find(&claims).Error)
	require.Len(t, claims, 1)
	assert.Equal(t, int64(1000), claims[0].AmountMloki)
	assert.Nil(t, claims[0].ClaimedAt)
}

func TestCreate_MultipleRecipients_OneSharedWallet_CustomAmounts_SharedExpiry(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pk2, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	connKey := tests.RandomHex32()
	iaPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	registerTrustedIA(t, svc, iaPubkey)

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk1, AmountMloki: 1000},
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk2, AmountMloki: 2500},
			{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: connKey, IAPubkey: iaPubkey, AmountMloki: 500},
		},
		ExpirySecs: 900,
	})
	require.NoError(t, err)

	// Exactly one shared wallet app, not three.
	var childApps []db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps).Error)
	require.Len(t, childApps, 1)
	assert.Equal(t, result.WalletApp.ID, childApps[0].ID)
	// MaxAmountLoki funded is the SUM (1000+2500+500 = 4000 mloki = 4 loki).
	var perm db.AppPermission
	require.NoError(t, svc.DB.Where("app_id = ? AND scope = ?", childApps[0].ID, constants.JIT_CLAIM_FUNDS_SCOPE).First(&perm).Error)
	assert.Equal(t, 4, perm.MaxAmountLoki)

	// Three independent claim rows, each with its own amount, all sharing one expiry.
	var claims []db.JITWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", childApps[0].ID).Find(&claims).Error)
	require.Len(t, claims, 3)
	byIdentity := map[string]db.JITWalletClaim{}
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

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)

	_, err = Create(context.TODO(), newTestDeps(svc), Params{HubApp: hub, Recipients: []RecipientInput{}})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_TooManyRecipients_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000_000, 3600)
	tests.FundApp(svc, hub.ID, 100_000_000, "fundtxhash")

	recipients := make([]RecipientInput, maxRecipientsPerWallet+1)
	for i := range recipients {
		pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
		recipients[i] = RecipientInput{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk, AmountMloki: 1}
	}

	_, err = Create(context.TODO(), newTestDeps(svc), Params{HubApp: hub, Recipients: recipients})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_DuplicateIdentityInBatch_Rejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk, AmountMloki: 500},
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk, AmountMloki: 500},
		},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_ConnectionKeyMode_MissingIAPubkey(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: tests.RandomHex32(), AmountMloki: 1000},
		},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_ConnectionKeyMode_InvalidHex(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	iaPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: "not-valid-hex!", IAPubkey: iaPubkey, AmountMloki: 1000},
		},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
}

func TestCreate_ConnectionKeyMode_UntrustedIARejected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	iaPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	// Deliberately not registered via registerTrustedIA.

	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.JITAllocIdentityConnectionKey, IdentityValue: tests.RandomHex32(), IAPubkey: iaPubkey, AmountMloki: 1000},
		},
		ExpirySecs: 1800,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrInvalidParams))
	assert.Contains(t, err.Error(), "not a trusted Identity Authority")
}

func TestCreate_NotJITHub(t *testing.T) {
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

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
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

	hub := tests.CreateJITHub(t, svc, 5000, 3600) // per-wallet (total) max 5000 mloki
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pk2, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	// Each recipient individually under the cap, but the sum (3000+3000=6000) exceeds it.
	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk1, AmountMloki: 3000},
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk2, AmountMloki: 3000},
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

	hub := tests.CreateJITHub(t, svc, 1_000_000, 3600) // per-wallet (total) max 1,000,000 mloki
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pk2, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	_, err = Create(context.TODO(), newTestDeps(svc), Params{
		HubApp: hub,
		Recipients: []RecipientInput{
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk1, AmountMloki: math.MaxUint64 - 500},
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk2, AmountMloki: 1000},
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

	hub := tests.CreateJITHub(t, svc, 100_000, 3600) // max 3600 secs

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

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	result, err := Create(context.TODO(), newTestDeps(svc), Params{
		HubApp:     hub,
		Recipients: onePubkeyRecipient(1000),
		// ExpirySecs omitted (zero value).
	})
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(3600*time.Second), result.ExpiresAt, 5*time.Second,
		"wallet must default to the hub's max_exp_secs, not expire immediately")
}

func TestCreate_TransferFailure_Rollback(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
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
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps)
	assert.Empty(t, childApps, "the child JIT wallet app must be rolled back after a funding failure")

	var claims []db.JITWalletClaim
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

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
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
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps).Error)
	assert.Len(t, childApps, 2)
}
