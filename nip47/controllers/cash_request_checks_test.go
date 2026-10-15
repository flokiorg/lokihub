package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

// NIP-CASH §Redeeming a Slice: for an identity-bound slice, identity_type,
// identity_value and identity_event are REQUIRED together
// (CASH-redeeming-a-slice-05). Each one missing on its own is refused.
func TestHandleCashRedeemEvent_IdentityFieldsRequiredTogether(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, 1000)
	claimantPriv := nostr.GeneratePrivateKey()
	claimant, _ := nostr.GetPublicKey(claimantPriv)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimant, AmountMloki: 1000},
	}))
	proof := mustMarshal(t, buildClaimProofEvent(t, claimantPriv, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now()))

	for name, request := range map[string]nipcash.CashRedeemRequest{
		"no identity_value": {Invoice: tests.MockZeroAmountInvoice, Amount: ptrUint64(1000), IdentityType: db.CashIdentityPubkey, IdentityEvent: proof},
		"no identity_event": {Invoice: tests.MockZeroAmountInvoice, Amount: ptrUint64(1000), IdentityType: db.CashIdentityPubkey, IdentityValue: claimant},
	} {
		response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, request)
		require.NotNil(t, response.Error, name)
		assert.Equal(t, constants.ERROR_BAD_REQUEST, response.Error.Code, name)
	}
	assert.Empty(t, svc.LNClient.(*tests.MockLn).SendPaymentSyncCalls(), "nothing is paid")
}

// NIP-CASH §Consolidating Tokens: a request that fails steps 1-6 MUST be
// rejected before step 7 (CASH-consolidating-tokens-10), so a refusal leaves
// every source exactly as it was: unclaimed, and with its proof still unused.
func TestConsolidate_OverTheCeiling_RefusedBeforeAnySourceIsClaimed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600) // per-wallet ceiling 100,000
	caller := guardCashWallet(t, svc, hub)
	ownerPriv := nostr.GeneratePrivateKey()
	ownerPub, _ := nostr.GetPublicKey(ownerPriv)
	_, secretHash, err := cashwallet.GenerateCashSecret()
	require.NoError(t, err)

	const amount1, amount2 = uint64(60_000), uint64(50_000) // 110,000 merged
	s1 := guardSource(t, svc, hub, ownerPub, amount1, 0, 0)
	tests.FundApp(svc, s1.ID, 200_000, "s1-fund")
	s2 := guardSource(t, svc, hub, ownerPub, amount2, 0, 0)
	tests.FundApp(svc, s2.ID, 200_000, "s2-fund")

	proof1 := buildTransferProofEvent(t, ownerPriv, *s1.WalletPubkey, db.CashIdentityCash, secretHash, "", amount1, nil, time.Now())
	proof2 := buildTransferProofEvent(t, ownerPriv, *s2.WalletPubkey, db.CashIdentityCash, secretHash, "", amount2, nil, time.Now())
	resp := handleCashConsolidateFor(t, svc, NewTestNip47Controller(svc), caller, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: *s1.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof1)},
			{WalletPubkey: *s2.WalletPubkey, IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPub, IdentityEvent: mustMarshal(t, proof2)},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash, IdentityValue: secretHash},
	})
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)

	for _, source := range []*db.App{s1, s2} {
		assert.Nil(t, cashWalletClaimByIdentity(t, svc, source.ID, db.CashIdentityPubkey, ownerPub).ClaimedAt)
	}
	var used int64
	require.NoError(t, svc.DB.Model(&db.CashTransferProof{}).Count(&used).Error)
	assert.Zero(t, used)
}

// NIP-CASH §Security Considerations for Cash-Mode Slices: a throttled call MUST
// get a distinct rate-limit error (CASH-security-considerations-for-cash-12).
// Redeem's is pinned by TestHandleCashRedeemEvent_RateLimited; these are
// transfer and consolidate, which share its limiter.
func TestCashTransferAndConsolidate_ThrottledCallsAreRateLimited(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := guardCashWallet(t, svc, hub)
	controller := NewTestNip47Controller(svc)
	limit := svc.Cfg.GetEnv().CashWalletClaimRateLimitPerHour
	require.Positive(t, limit)
	for i := 0; i < limit; i++ {
		controller.cashClaimLimiter.Allow(wallet.AppPubkey, limit)
	}

	transfer := handleCashTransferFor(t, svc, controller, wallet, cashTransferParams{
		IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), IdentityEvent: "{}",
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32()},
	})
	require.NotNil(t, transfer.Error)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, transfer.Error.Code)

	consolidate := handleCashConsolidateFor(t, svc, controller, wallet, cashConsolidateParams{
		Sources: []consolidateSourceParam{
			{WalletPubkey: tests.RandomHex32(), IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), IdentityEvent: "{}"},
			{WalletPubkey: tests.RandomHex32(), IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32(), IdentityEvent: "{}"},
		},
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: tests.RandomHex32()},
	})
	require.NotNil(t, consolidate.Error)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, consolidate.Error.Code)
}
