package controllers

// Closing pass: a redemption whose payout is zero must be refused, not performed.
//
// Reachable because the redeem fee grew a flat base this round.
// CalculateRedeemFeeMloki saturates at the amount it is charged on, so a slice at or
// below the base quotes a fee equal to the whole slice and a payout of zero — and
// nothing downstream stopped that. The exact-match check compares the invoice amount
// against expectedAmount, and an amountless invoice resolves to 0, so a zero payout
// MATCHED. The slice was consumed to pay an invoice for nothing: the holder lost the
// slice, received no payment, and got a SUCCESS response.
//
// CalculateRedeemFeeMloki's own doc comment used to promise that a mint-time
// MinRedeemableMloki refused this case. No such guard was ever implemented — the
// comment was the only thing asserting it, which is why this sat open. Mint time is
// also the wrong place: whether a slice can pay out depends on IsSelfPayment, known
// only at redeem, and a slice below the base fee is still transferable,
// consolidatable, and redeemable same-node where the fee is waived.
//
// The matrix is over the two things that decide a payout — how the fee is composed
// (base, proportional, or both) and whether the redemption resolves same-node — plus
// the boundary on each side of zero. Every refusal row also asserts the slice is
// still there afterwards, because a guard that refuses and consumes anyway would be
// no better than the bug.

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/ohstr/nmilat/nipcash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/tests"
)

func TestCashRedeem_ZeroPayout_RefusedAndSliceSurvives(t *testing.T) {
	cases := []struct {
		name string
		// the slice and the fee schedule snapshotted onto its claim
		sliceMloki   int64
		feeBaseMloki int64
		feePpm       int
		// what the caller asks to be paid
		invoiceAmount uint64
		// whether the redemption resolves to a same-node payment (fee waived)
		sameNode bool

		wantRefused bool
		why         string
	}{
		{
			name: "base_above_slice_amountless_invoice", sliceMloki: 500, feeBaseMloki: 1000,
			invoiceAmount: 0, wantRefused: true,
			why: "the bug's exact shape: fee saturates to 500, payout 0, and an amountless " +
				"invoice's 0 MATCHES it, so the old code paid nothing and consumed the slice",
		},
		{
			name: "base_equals_slice_amountless_invoice", sliceMloki: 1000, feeBaseMloki: 1000,
			invoiceAmount: 0, wantRefused: true,
			why: "the boundary: fee == slice exactly, payout 0",
		},
		{
			name: "base_one_below_slice_pays_one", sliceMloki: 1001, feeBaseMloki: 1000,
			invoiceAmount: 1, wantRefused: false,
			why: "the other side of the boundary: a payout of 1 millis is still a payout, " +
				"and refusing it would be the guard overreaching",
		},
		{
			name: "proportional_hundred_percent", sliceMloki: 1000, feePpm: 1_000_000,
			invoiceAmount: 0, wantRefused: true,
			why: "same zero payout with no base at all, so the guard cannot be keyed on the base",
		},
		{
			name: "base_plus_proportional_together_consume_it", sliceMloki: 1000, feeBaseMloki: 900, feePpm: 100_000,
			invoiceAmount: 0, wantRefused: true,
			why: "900 base + 100 proportional == 1000; neither component alone would reach it",
		},
		{
			name: "base_above_slice_but_same_node", sliceMloki: 500, feeBaseMloki: 1000,
			invoiceAmount: 500, sameNode: true, wantRefused: false,
			why: "a same-node redemption waives the fee entirely, so this slice pays out in " +
				"full — the case that makes refusing at MINT time wrong",
		},
		{
			name: "base_above_slice_full_amount_invoice", sliceMloki: 500, feeBaseMloki: 1000,
			invoiceAmount: 500, wantRefused: true,
			why: "asking for the full amount on a zero-payout slice must still be refused; " +
				"it is the payout that is impossible, not the amount that is mismatched",
		},
		{
			name: "no_fee_at_all", sliceMloki: 1000,
			invoiceAmount: 1000, wantRefused: false,
			why: "the control: with no fee configured nothing about this path changes",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			if c.sameNode {
				// Makes transactions.IsSelfPayment resolve MockZeroAmountInvoice as
				// same-node: the mock node's own pubkey is the invoice's payee, and a
				// matching incoming row exists.
				svc.LNClient.(*tests.MockLn).Pubkey = mockZeroAmountInvoicePayeePubkey
				payee, _, appErr := tests.CreateApp(svc)
				require.NoError(t, appErr)
				preimage := "zero-payout-preimage"
				require.NoError(t, svc.DB.Create(&db.Transaction{
					State:          "PENDING",
					Type:           "incoming",
					PaymentRequest: tests.MockZeroAmountInvoice,
					PaymentHash:    tests.MockZeroAmountPaymentHash,
					Preimage:       &preimage,
					AmountMloki:    uint64(c.sliceMloki), //nolint:gosec // every row's sliceMloki is a positive literal
					AppId:          &payee.ID,
				}).Error)
			}

			hub := tests.CreateCashHub(t, svc, 100_000, 3600)
			wallet := newFundedCashWallet(t, svc, hub, c.sliceMloki)

			claimantPrivkey := nostr.GeneratePrivateKey()
			claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
			require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{{
				IdentityType:       db.CashIdentityPubkey,
				IdentityValue:      claimantPubkey,
				AmountMloki:        c.sliceMloki,
				RedeemFeeBaseMloki: c.feeBaseMloki,
				RedeemFeePpm:       c.feePpm,
			}}))

			proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
			amount := c.invoiceAmount
			response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, nipcash.CashRedeemRequest{
				Invoice:       tests.MockZeroAmountInvoice,
				Amount:        &amount,
				IdentityType:  db.CashIdentityPubkey,
				IdentityValue: claimantPubkey,
				IdentityEvent: mustMarshal(t, proof),
			})

			// Read the claim row directly rather than through GetCashWalletClaim,
			// which filters on claimed_at IS NULL and so returns nothing at all for an
			// accepted redemption — the state this test has to distinguish.
			var claim db.CashWalletClaim
			require.NoError(t, svc.DB.
				Where("wallet_app_id = ? AND identity_type = ? AND identity_value = ?",
					wallet.ID, db.CashIdentityPubkey, claimantPubkey).
				First(&claim).Error)

			if !c.wantRefused {
				require.Nil(t, response.Error, "must be accepted — %s", c.why)
				assert.NotNil(t, claim.ClaimedAt, "an accepted redemption claims the slice")
				return
			}

			require.NotNil(t, response.Error, "must be refused — %s", c.why)
			// The slice must still be there. A guard that refuses the payment but
			// leaves the claim consumed destroys the money just as thoroughly, only
			// with a clearer error message.
			assert.Nil(t, claim.ClaimedAt,
				"the slice was consumed by a refused redemption — the claim must be rolled back so "+
					"the holder can still consolidate it and redeem the total (%s)", c.why)
			// And nothing was paid.
			var outgoing int64
			require.NoError(t, svc.DB.Model(&db.Transaction{}).
				Where("type = ?", "outgoing").Count(&outgoing).Error)
			assert.Zero(t, outgoing, "a refused redemption must not have paid anything")
		})
	}
}
