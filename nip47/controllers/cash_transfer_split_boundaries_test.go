package controllers

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/tests"
)

// TestCashTransferSplit_AmountAndFloorBoundaries tables every way a partial
// split's requested amount can be rejected BEFORE any funds move: a
// non-positive amount, an amount larger than the caller's own slice, and either
// side of the split falling below the slice's inherited min_transfer_mloki
// floor. Each case both proves the guard fires AND that the source slice is left
// completely untouched (not claimed, unchanged amount) — a rejected split must
// never leak value or strand a slice.
func TestCashTransferSplit_AmountAndFloorBoundaries(t *testing.T) {
	const sliceAmount = uint64(5000)
	cases := []struct {
		name        string
		floor       int64
		requested   uint64
		wantMsgPart string
	}{
		{name: "zero amount", floor: 0, requested: 0, wantMsgPart: "positive"},
		{name: "exceeds own slice", floor: 0, requested: sliceAmount + 1, wantMsgPart: "exceeds"},
		{name: "carved below floor", floor: 1000, requested: 500, wantMsgPart: "min_transfer_mloki floor"},
		{name: "remainder below floor", floor: 1000, requested: 4500, wantMsgPart: "remainder"},
		{name: "carved exactly one below floor", floor: 2000, requested: 1999, wantMsgPart: "min_transfer_mloki floor"},
		{name: "remainder exactly one below floor", floor: 2000, requested: 3001, wantMsgPart: "remainder"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			hub := tests.CreateCashHub(t, svc, 100_000, 3600)
			wallet := newFundedCashWallet(t, svc, hub, int64(sliceAmount))
			curPriv := nostr.GeneratePrivateKey()
			curPub, _ := nostr.GetPublicKey(curPriv)
			require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
				{IdentityType: db.CashIdentityPubkey, IdentityValue: curPub, AmountMloki: int64(sliceAmount), MinTransferMloki: tc.floor},
			}))

			newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
			proof := buildTransferProofEvent(t, curPriv, *wallet.WalletPubkey, db.CashIdentityPubkey, newPub, "", tc.requested, nil, time.Now())
			amt := tc.requested
			resp := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
				IdentityType:  db.CashIdentityPubkey,
				IdentityValue: curPub,
				IdentityEvent: mustMarshal(t, proof),
				NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
				AmountMloki:   &amt,
			})
			require.NotNil(t, resp.Error)
			assert.Equal(t, constants.ERROR_BAD_REQUEST, resp.Error.Code)
			assert.Contains(t, resp.Error.Message, tc.wantMsgPart)

			// The source slice must be completely untouched by a rejected split.
			claim := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, curPub)
			require.NotNil(t, claim)
			assert.Nil(t, claim.ClaimedAt, "a rejected split must not claim the slice")
			assert.Equal(t, int64(sliceAmount), claim.AmountMloki, "a rejected split must not change the slice amount")
		})
	}
}

// TestCashTransferSplit_ExactFloorBoundaries_Succeed is the positive companion:
// a split whose carved piece AND remainder each land exactly on the floor is
// allowed, consuming the source into two fresh wallets. (Needs a real two-hop
// funding, so it queues two distinct-payment-hash invoices.)
func TestCashTransferSplit_ExactFloorBoundaries_Succeed(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.CASH_CONSOLIDATE_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, 200_000, tests.RandomHex32())
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "p-carved", Amount: 2000},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, PaymentHash: tests.MockLNClientHoldTransaction.PaymentHash, Preimage: "p-rem", Amount: 2000},
	}

	// floor 2000, slice 4000, carve 2000 -> remainder 2000: both exactly the floor.
	curPriv := nostr.GeneratePrivateKey()
	curPub, _ := nostr.GetPublicKey(curPriv)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: curPub, AmountMloki: 4000, MinTransferMloki: 2000},
	}))

	newPub, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	amt := uint64(2000)
	proof := buildTransferProofEvent(t, curPriv, *wallet.WalletPubkey, db.CashIdentityPubkey, newPub, "", 2000, nil, time.Now())
	resp := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: curPub,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPub},
		AmountMloki:   &amt,
	})
	require.Nil(t, resp.Error)
	result := resp.Result.(cashTransferResponse)
	assert.EqualValues(t, 2000, result.AmountMloki)
	require.NotNil(t, result.RemainingAmountMloki)
	assert.EqualValues(t, 2000, *result.RemainingAmountMloki)
	require.NotEmpty(t, result.NewWalletToken)
	require.NotEmpty(t, result.RemainderWalletToken)

	claim := cashWalletClaimByIdentity(t, svc, wallet.ID, db.CashIdentityPubkey, curPub)
	require.NotNil(t, claim.ClaimedAt, "an exact-floor split still consumes the source slice")
}
