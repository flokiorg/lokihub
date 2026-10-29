package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/nip47/models"
	"github.com/flokiorg/lokihub/tests"
)

// transferWith runs cash_transfer on controller.
func transferWith(t *testing.T, controller *nip47Controller, wallet *db.App, params cashTransferParams) *models.Response {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"method": constants.NIP47MethodCashTransfer, "params": params})
	require.NoError(t, err)
	req := &models.Request{}
	require.NoError(t, json.Unmarshal(raw, req))
	var resp *models.Response
	controller.HandleCashTransferEvent(context.TODO(), req, 0, wallet,
		func(r *models.Response, _ nostr.Tags) { resp = r }, nostr.Tags{}, testPrivateCall(params.IdentityEvent))
	require.NotNil(t, resp)
	return resp
}

func partialSplitParams(t *testing.T, wallet *db.App, ownerPriv string, amount uint64) cashTransferParams {
	t.Helper()
	owner, _ := nostr.GetPublicKey(ownerPriv)
	to, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	proof := buildTransferProofEvent(t, ownerPriv, *wallet.WalletPubkey, db.CashIdentityPubkey, to, "", amount, nil, time.Now())
	return cashTransferParams{
		IdentityType: db.CashIdentityPubkey, IdentityValue: owner, IdentityEvent: mustMarshal(t, proof),
		NewIdentity:  cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: to},
		AmountMillis: &amount, RequestID: testRequestID(),
	}
}

// H-CASH-212: spin-offs are bounded per bill lineage per hour. Two recipients of
// one minted bill share its lineage, so with a limit of 1 the second spin-off is
// RATE_LIMITED, after the mark, with nothing claimed.
func TestCashTransfer_LineageLimiter(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fund")
	alice, bob := nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey()
	alicePub, _ := nostr.GetPublicKey(alice)
	bobPub, _ := nostr.GetPublicKey(bob)
	controller := NewTestNip47Controller(svc).WithLineageLimiter(NewRateLimiter())
	res, err := cashwallet.Create(context.TODO(), controller.cashDeps(), cashwallet.Params{HubApp: hub, ExpirySecs: 1800,
		Recipients: []cashwallet.RecipientInput{
			{IdentityType: db.CashIdentityPubkey, IdentityValue: alicePub, AmountMloki: 5_000},
			{IdentityType: db.CashIdentityPubkey, IdentityValue: bobPub, AmountMloki: 5_000},
		}})
	require.NoError(t, err)
	bill := res.WalletApp
	require.NotNil(t, bill.CashLineageRootID)
	assert.Equal(t, bill.ID, *bill.CashLineageRootID, "a minted bill is its own lineage root")
	svc.Cfg.GetEnv().CashLineageSpinOffsPerHour = 1

	first := transferWith(t, controller, bill, partialSplitParams(t, bill, alice, 2_000))
	require.Nil(t, first.Error)
	var carved db.App
	require.NoError(t, svc.DB.Where("wallet_pubkey = ?", first.Result.(cashTransferResponse).NewWalletPubkey).First(&carved).Error)
	require.NotNil(t, carved.CashLineageRootID)
	assert.Equal(t, bill.ID, *carved.CashLineageRootID, "a spin-off keeps its source's lineage")

	p := partialSplitParams(t, bill, bob, 2_000)
	second := transferWith(t, controller, bill, p)
	require.NotNil(t, second.Error)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, second.Error.Code)
	assert.True(t, isUsed(t, svc, p.RequestID))
	assert.Nil(t, cashWalletClaimByIdentity(t, svc, bill.ID, db.CashIdentityPubkey, bobPub).ClaimedAt)
}

// R2-F4: a spin-off whose commit fails gives its lineage's allowance back, so
// with a limit of 1 the retry still goes through; the next one is limited.
func TestCashTransfer_LineageAllowanceSpentOnlyOnCommit(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()
	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	tests.FundApp(svc, hub.ID, 10_000_000, "fund")
	alice, bob := nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey()
	alicePub, _ := nostr.GetPublicKey(alice)
	bobPub, _ := nostr.GetPublicKey(bob)
	controller := NewTestNip47Controller(svc).WithLineageLimiter(NewRateLimiter())
	res, err := cashwallet.Create(context.TODO(), controller.cashDeps(), cashwallet.Params{HubApp: hub, ExpirySecs: 1800,
		Recipients: []cashwallet.RecipientInput{
			{IdentityType: db.CashIdentityPubkey, IdentityValue: alicePub, AmountMloki: 5_000},
			{IdentityType: db.CashIdentityPubkey, IdentityValue: bobPub, AmountMloki: 5_000},
		}})
	require.NoError(t, err)
	bill := res.WalletApp
	svc.Cfg.GetEnv().CashLineageSpinOffsPerHour = 1

	controller.cashHooks = &cashwallet.TestHooks{Before: func(stage string) error {
		if stage == cashwallet.StageAfterTransfer+"(0)" {
			return errors.New("injected: funding failed")
		}
		return nil
	}}
	failed := transferWith(t, controller, bill, partialSplitParams(t, bill, alice, 2_000))
	require.NotNil(t, failed.Error, "the commit fails")
	assert.NotEqual(t, constants.ERROR_RATE_LIMITED, failed.Error.Code)

	controller.cashHooks = nil
	retry := transferWith(t, controller, bill, partialSplitParams(t, bill, alice, 2_000))
	require.Nil(t, retry.Error, "the failed call spent no allowance")

	limited := transferWith(t, controller, bill, partialSplitParams(t, bill, bob, 2_000))
	require.NotNil(t, limited.Error)
	assert.Equal(t, constants.ERROR_RATE_LIMITED, limited.Error.Code)
}

// H-CASH-305: a spin-off that would take the Cash Hub past its live-bill cap is
// QUOTA_EXCEEDED; one that fits goes through.
func TestCashTransfer_LiveBillCap(t *testing.T) {
	f := newTransferFixture(t, 5_000)
	var live int64
	require.NoError(t, f.svc.DB.Model(&db.App{}).Where("parent_app_id = ? AND kind = ?", f.hub.ID, db.AppKindCashWallet).Count(&live).Error)
	f.svc.Cfg.GetEnv().CashMaxLiveBillsPerHub = int(live) + 1

	p := partialSplitParams(t, f.wallet, f.ownerPriv, 2_000) // two new bills
	resp := transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.NotNil(t, resp.Error)
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, resp.Error.Code)
	assert.Nil(t, cashWalletClaimByIdentity(t, f.svc, f.wallet.ID, db.CashIdentityPubkey, f.ownerPub).ClaimedAt)

	// A full transfer to cash on a bill with another recipient spins off one bill.
	_, commitment := cashSecretAndHash(t)
	proof := buildTransferProofEvent(t, f.ownerPriv, *f.wallet.WalletPubkey, db.CashIdentityCash, commitment, "", 5_000, nil, time.Now())
	other, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, f.svc.AppsService.CreateCashWalletClaims(f.wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: other, AmountMloki: 1},
	}))
	p = cashTransferParams{IdentityType: db.CashIdentityPubkey, IdentityValue: f.ownerPub, IdentityEvent: mustMarshal(t, proof),
		NewIdentity: cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash, IdentityValue: commitment}, RequestID: testRequestID()}
	resp = transferWithCall(t, f.svc, f.wallet, p, testPrivateCall(p.IdentityEvent))
	require.Nil(t, resp.Error, "one new bill fits under the cap")
	assert.NotEmpty(t, resp.Result.(cashTransferResponse).NewWalletPubkey)
}

// A commit refused by the live-bill cap it re-counts is QUOTA_EXCEEDED with a
// fixed message, like the early count; other commit failures keep their codes.
func TestCommitError_LiveBillCapIsQuotaExceeded(t *testing.T) {
	assert.Equal(t, constants.ERROR_QUOTA_EXCEEDED, mapCashWalletErrorCode(fmt.Errorf("commit: %w", cashwallet.ErrLiveBillCap)))
	assert.Contains(t, commitErrorMessage(cashwallet.ErrLiveBillCap), "maximum of live bills")
	assert.Equal(t, constants.ERROR_UNAUTHORIZED, mapCashWalletErrorCode(cashwallet.ErrProofUsed))
	assert.Equal(t, constants.ERROR_INTERNAL, mapCashWalletErrorCode(errors.New("node down")))
}
