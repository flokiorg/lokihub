package controllers

import (
	"errors"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/apps"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/nip47/permissions"
	"github.com/flokiorg/lokihub/tests"
	"github.com/flokiorg/lokihub/transactions"
)

// NIP-CASH §Lifecycle and Deletion: the auto-delete policy is the same for
// every drain mechanism. These are the two drains that end in a Hub-side fault
// after the funds have already moved; before, both left the drained bill alive
// until its expiry sweep (forever for a bill that never expires).

func newFaultTestController(svc *tests.TestService, txs transactions.TransactionsService, k keys.Keys) *nip47Controller {
	return NewNip47Controller(svc.LNClient, svc.DB, svc.EventPublisher,
		permissions.NewPermissionsService(svc.DB, svc.EventPublisher), txs, svc.AppsService, k, nil,
		NewRateLimiter(), NewRateLimiter(), NewRateLimiter(), svc.Cfg, apps.NewIdentityAuthorityManager(svc.DB))
}

func requireBillArchivedAndDeleted(t *testing.T, svc *tests.TestService, walletID uint) {
	t.Helper()
	var live int64
	require.NoError(t, svc.DB.Model(&db.App{}).Where("id = ?", walletID).Count(&live).Error)
	assert.Zero(t, live, "the drained bill must be deleted, so requests to it get silence")
	var archived db.CashBillArchive
	require.NoError(t, svc.DB.Where("wallet_app_id = ?", walletID).First(&archived).Error)
	assert.Equal(t, db.CashBillOutcomeDrained, archived.Outcome)
}

// preimageLostTransactions pays for real but hands the controller back a
// settled transaction without its preimage.
type preimageLostTransactions struct {
	transactions.TransactionsService
}

func (s preimageLostTransactions) SendPaymentSync(payReq string, amountMloki *uint64, metadata map[string]interface{}, lnClient lnclient.LNClient, appId *uint, requestEventId *uint) (*transactions.Transaction, error) {
	tx, err := s.TransactionsService.SendPaymentSync(payReq, amountMloki, metadata, lnClient, appId, requestEventId)
	if tx != nil {
		lost := *tx
		lost.Preimage = nil
		return &lost, err
	}
	return tx, err
}

func TestHandleCashRedeemEvent_SettledWithoutPreimage_DrainedBillIsDeleted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	// Funded to exactly the slice, so the payout leaves zero (see
	// TestHandleCashRedeemEvent_DrainedBillIsArchivedAndDeleted).
	const sliceMloki = 1000
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", sliceMloki, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, sliceMloki, tests.RandomHex32())

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: claimantPubkey, AmountMloki: sliceMloki},
	}))

	controller := newFaultTestController(svc,
		preimageLostTransactions{transactions.NewTransactionsService(svc.DB, svc.EventPublisher)}, svc.Keys)
	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	response := handleClaimFundsFor(t, svc, controller, wallet, nipcash.CashRedeemRequest{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        ptrUint64(sliceMloki),
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})
	require.NotNil(t, response.Error, "the caller still learns the preimage is unavailable")

	requireBillArchivedAndDeleted(t, svc, wallet.ID)
}

// walletKeyLost cannot derive the wallet key of any app created after the
// fixture, which is exactly the key a split needs to deliver the new
// wallet's connection.
type walletKeyLost struct {
	keys.Keys
	lostAbove uint
}

func (k walletKeyLost) GetAppWalletKey(childIndex uint) (string, error) {
	if childIndex > k.lostAbove {
		return "", errors.New("wallet key unavailable")
	}
	return k.Keys.GetAppWalletKey(childIndex)
}

func TestHandleCashTransferEvent_SpinOffNotDelivered_DrainedSourceIsDeleted(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// The spin-off pays tests.MockInvoice, which is fixed at 123,000 mloki, so
	// the moving slice is that amount and the source is funded with exactly
	// it: the spin-off leaves the source at zero.
	const sliceMloki = 123_000
	hub := tests.CreateCashHub(t, svc, 1_000_000, 3600)
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", sliceMloki/1000, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, sliceMloki, tests.RandomHex32())

	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c" // MockInvoice's payee: settles as a self-payment
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-spinoff", Amount: sliceMloki},
	}

	// Two recipients, so the transfer spins off instead of reassigning in
	// place. The other slice is already claimed, so the moving slice is the
	// bill's last unclaimed one.
	ownerPrivkey := nostr.GeneratePrivateKey()
	ownerPubkey, _ := nostr.GetPublicKey(ownerPrivkey)
	otherPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: ownerPubkey, AmountMloki: sliceMloki},
		{IdentityType: db.CashIdentityPubkey, IdentityValue: otherPubkey, AmountMloki: 1000},
	}))
	_, err = svc.AppsService.ClaimCashSlice(wallet.ID, db.CashIdentityPubkey, otherPubkey)
	require.NoError(t, err)

	_, newSecretHash := cashSecretAndHash(t)
	proof := buildTransferProofEvent(t, ownerPrivkey, *wallet.WalletPubkey, db.CashIdentityCash, newSecretHash, "", sliceMloki, nil, time.Now())
	controller := newFaultTestController(svc, transactions.NewTransactionsService(svc.DB, svc.EventPublisher),
		walletKeyLost{Keys: svc.Keys, lostAbove: wallet.ID})
	response := handleCashTransferFor(t, svc, controller, wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: ownerPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityCash, IdentityValue: newSecretHash},
	})
	require.NotNil(t, response.Error, "the new wallet's connection could not be delivered")
	assert.Contains(t, response.Error.Message, "could not be delivered")

	requireBillArchivedAndDeleted(t, svc, wallet.ID)
}
