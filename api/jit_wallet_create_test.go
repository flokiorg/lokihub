package api

import (
	"errors"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/tests/mocks"
	"github.com/flokiorg/lokihub/transactions"
)

// newTestAPI returns an api wired to the test service's real DB and AppsService.
func newTestAPI(svc *tests.TestService) *api {
	return &api{db: svc.DB, appsSvc: svc.AppsService, keys: svc.Keys, cfg: svc.Cfg}
}

// newTestAPIWithService returns an api wired to the test service's real DB,
// AppsService, and a mock service.Service that forwards GetTransactionsService
// and GetLNClient to the test service's real instances — everything
// jitwallet.Create needs beyond what newTestAPI already provides.
func newTestAPIWithService(t *testing.T, svc *tests.TestService) *api {
	mockSvc := mocks.NewMockService(t)
	txSvc := transactions.NewTransactionsService(svc.DB, svc.EventPublisher)
	mockSvc.On("GetTransactionsService").Return(txSvc).Maybe()
	mockSvc.On("GetLNClient").Return(svc.LNClient).Maybe()

	return &api{
		db:        svc.DB,
		appsSvc:   svc.AppsService,
		keys:      svc.Keys,
		cfg:       svc.Cfg,
		svc:       mockSvc,
		iaManager: apps.NewIdentityAuthorityManager(svc.DB),
	}
}

func TestCreateJITWallet_HappyPath_SingleRecipient(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	theAPI := newTestAPIWithService(t, svc)
	result, err := theAPI.CreateJITWallet(hub.ID, &CreateJITWalletRequest{
		Recipients: []JITWalletRecipient{
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: beneficiaryPubkey, AmountMloki: 1000},
		},
		ExpirySecs: 1800,
	})
	require.NoError(t, err)
	assert.Contains(t, result.PairingURI, "nostr+walletconnect://")
	assert.NotZero(t, result.AppID)
	require.Len(t, result.Recipients, 1)

	var childApp db.App
	require.NoError(t, svc.DB.First(&childApp, result.AppID).Error)
	assert.Equal(t, db.AppKindJITWallet, childApp.Kind)
	assert.Equal(t, hub.ID, *childApp.ParentAppID)

	var claimCount int64
	require.NoError(t, svc.DB.Model(&db.JITWalletClaim{}).Where("wallet_app_id = ?", result.AppID).Count(&claimCount).Error)
	assert.EqualValues(t, 1, claimCount)

	// GET jit-connection for the new wallet must return the identical URI (determinism).
	conn, err := theAPI.GetJITWalletConnection(result.AppID)
	require.NoError(t, err)
	assert.Equal(t, result.PairingURI, conn.PairingURI)
}

func TestCreateJITWallet_HappyPath_MultipleRecipients_OneSharedWallet(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	pk1, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	pk2, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	theAPI := newTestAPIWithService(t, svc)
	result, err := theAPI.CreateJITWallet(hub.ID, &CreateJITWalletRequest{
		Recipients: []JITWalletRecipient{
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk1, AmountMloki: 1000},
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: pk2, AmountMloki: 2000},
		},
		ExpirySecs: 1800,
	})
	require.NoError(t, err)
	require.Len(t, result.Recipients, 2)

	var childApps []db.App
	require.NoError(t, svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps).Error)
	require.Len(t, childApps, 1, "both recipients must share exactly one wallet app")

	var claims []db.JITWalletClaim
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", childApps[0].ID).Find(&claims).Error)
	require.Len(t, claims, 2)
}

func TestCreateJITWallet_ChildExcludedFromListApps(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	beneficiaryPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	theAPI := newTestAPIWithService(t, svc)
	result, err := theAPI.CreateJITWallet(hub.ID, &CreateJITWalletRequest{
		Recipients: []JITWalletRecipient{
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: beneficiaryPubkey, AmountMloki: 1000},
		},
		ExpirySecs: 1800,
	})
	require.NoError(t, err)

	// jit_wallet children are ephemeral, spend-only wallets managed via their
	// hub's claims list — they must never show up in the general Connections
	// page listing.
	listResp, err := theAPI.ListApps(100, 0, ListAppsFilters{}, "")
	require.NoError(t, err)
	for _, a := range listResp.Apps {
		assert.NotEqual(t, result.AppID, a.ID, "jit_wallet child app must not appear in ListApps")
		assert.NotEqual(t, db.AppKindJITWallet, a.Kind, "no jit_wallet app should ever appear in ListApps")
	}
}

func TestCreateJITWallet_InsufficientBalance(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	// hub has 0 balance — do NOT fund it.

	beneficiaryPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	theAPI := newTestAPIWithService(t, svc)
	_, err = theAPI.CreateJITWallet(hub.ID, &CreateJITWalletRequest{
		Recipients: []JITWalletRecipient{
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: beneficiaryPubkey, AmountMloki: 5000},
		},
		ExpirySecs: 1800,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, transactions.NewInsufficientBalanceError()))

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps)
	assert.Empty(t, childApps)
}

func TestCreateJITWallet_NotJITHub(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	isolatedApp, _, err := svc.AppsService.CreateApp(
		"iso", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.GET_INFO_SCOPE}, db.AppKindIsolated, nil, "", nil,
	)
	require.NoError(t, err)

	beneficiaryPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	theAPI := newTestAPIWithService(t, svc)
	_, err = theAPI.CreateJITWallet(isolatedApp.ID, &CreateJITWalletRequest{
		Recipients: []JITWalletRecipient{
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: beneficiaryPubkey, AmountMloki: 1000},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a jit_hub")
}

func TestCreateJITWallet_TransferFailure_Rollback(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateJITHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fundtxhash")

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.PayInvoiceResponses = []*lnclient.PayInvoiceResponse{nil}
	mockLN.PayInvoiceErrors = []error{errors.New("simulated payment failure")}

	beneficiaryPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())

	theAPI := newTestAPIWithService(t, svc)
	_, err = theAPI.CreateJITWallet(hub.ID, &CreateJITWalletRequest{
		Recipients: []JITWalletRecipient{
			{IdentityType: db.JITAllocIdentityPubkey, IdentityValue: beneficiaryPubkey, AmountMloki: 1000},
		},
		ExpirySecs: 1800,
	})
	require.Error(t, err)

	var childApps []db.App
	svc.DB.Where("parent_app_id = ? AND kind = ?", hub.ID, db.AppKindJITWallet).Find(&childApps)
	assert.Empty(t, childApps, "the child JIT wallet app must be rolled back after a funding failure")
}
