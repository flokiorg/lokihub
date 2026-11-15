package controllers

// U-8.2: every external redemption caps its route at the quoted fee plus the
// Hub's margin, max(CASH_REDEEM_ROUTE_MARGIN_PPM × amount / 1e6,
// CASH_REDEEM_ROUTE_MARGIN_MIN_MILLIS), never the generic reserve (NIP-CASH
// §Redeeming a Slice step 4). It reads tests.MockLn's recordings
// (SendPaymentSyncCalls) so the cap is checked where it reaches the node.

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

func TestCashRedeem_RouteCapReachesTheNode(t *testing.T) {
	cases := []struct {
		name         string
		sliceMloki   int64
		feeBaseMloki int64
		feePpm       int
		sameNode     bool

		wantPayout uint64
		// nil means "no cap passed", i.e. the node keeps its own fee reserve
		wantRouteCap *uint64
		why          string
	}{
		{
			name:       "proportional_fee_plus_the_minimum_margin",
			sliceMloki: 1000, feePpm: 100_000, // 10% => fee 100
			wantPayout: 900, wantRouteCap: ptrUint64(1100),
			why: "fee 100 + max(2000 ppm of 1000, 1000)",
		},
		{
			name:       "flat_base_plus_the_minimum_margin",
			sliceMloki: 10_000, feeBaseMloki: 500,
			wantPayout: 9500, wantRouteCap: ptrUint64(1500),
			why: "fee 500 + max(20, 1000)",
		},
		{
			name:       "base_plus_proportional_plus_margin",
			sliceMloki: 10_000, feeBaseMloki: 500, feePpm: 100_000, // 500 + 1000
			wantPayout: 8500, wantRouteCap: ptrUint64(2500),
			why: "fee 1500 + max(20, 1000)",
		},
		{
			name:       "no_fee_is_still_capped_by_the_margin",
			sliceMloki: 1000,
			wantPayout: 1000, wantRouteCap: ptrUint64(1000),
			why: "a Hub with no fee still bounds its routing loss, never the generic reserve",
		},
		{
			name:       "a_large_slice_takes_the_proportional_margin",
			sliceMloki: 10_000_000,
			wantPayout: 10_000_000, wantRouteCap: ptrUint64(20_000),
			why: "2000 ppm of 10 000 000 is over the 1000 minimum",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, err := tests.CreateTestService(t)
			require.NoError(t, err)
			defer svc.Remove()

			mockLn := svc.LNClient.(*tests.MockLn)

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
			payout := c.wantPayout
			response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, nipcash.CashRedeemRequest{
				Invoice:       tests.MockZeroAmountInvoice,
				Amount:        &payout,
				IdentityType:  db.CashIdentityPubkey,
				IdentityValue: claimantPubkey,
				IdentityEvent: mustMarshal(t, proof),
			})
			require.Nil(t, response.Error, "redeem must succeed — %s", c.why)

			calls := mockLn.SendPaymentSyncCalls()
			require.Len(t, calls, 1, "exactly one payment attempt")

			require.NotNil(t, calls[0].Amount, "an amount must be passed to the node")
			assert.Equal(t, c.wantPayout, *calls[0].Amount,
				"the node was asked to pay the wrong amount — %s", c.why)

			if c.wantRouteCap == nil {
				assert.Nil(t, calls[0].FeeLimitMloki,
					"a cap was passed where none was withheld — %s", c.why)
				return
			}
			require.NotNil(t, calls[0].FeeLimitMloki,
				"NO route cap reached the node, so the Hub is back to paying for the "+
					"holder's chosen route out of its own balance — %s", c.why)
			assert.Equal(t, *c.wantRouteCap, *calls[0].FeeLimitMloki,
				"the route cap is not fee + margin — %s", c.why)

		})
	}
}

// TestCashRedeem_SameNode_WithholdsNothingAndCapsNothing is the other side of the
// waiver. A same-node redemption has no route to pay for, so the full slice must be
// paid out AND no cap may be imposed — a cap on a fee-free payment would be a cap of
// zero, which LND treats as "no fee permitted" rather than "no limit", and a payment
// that needs even one millisat of fee would fail.
func TestCashRedeem_SameNode_WithholdsNothingAndCapsNothing(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	mockLn := svc.LNClient.(*tests.MockLn)
	mockLn.Pubkey = mockZeroAmountInvoicePayeePubkey

	payee, _, err := tests.CreateApp(svc)
	require.NoError(t, err)
	preimage := "route-cap-same-node-preimage"
	const amountMloki = 1000
	require.NoError(t, svc.DB.Create(&db.Transaction{
		State:          "PENDING",
		Type:           "incoming",
		PaymentRequest: tests.MockZeroAmountInvoice,
		PaymentHash:    tests.MockZeroAmountPaymentHash,
		Preimage:       &preimage,
		AmountMloki:    amountMloki,
		AppId:          &payee.ID,
	}).Error)

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet := newFundedCashWallet(t, svc, hub, amountMloki)

	claimantPrivkey := nostr.GeneratePrivateKey()
	claimantPubkey, _ := nostr.GetPublicKey(claimantPrivkey)
	// A fee IS configured — the point is that same-node waives it anyway.
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		AmountMloki:   amountMloki,
		RedeemFeePpm:  100_000,
	}}))

	proof := buildClaimProofEvent(t, claimantPrivkey, *wallet.WalletPubkey, tests.MockZeroAmountPaymentHash, nil, time.Now())
	full := uint64(amountMloki)
	response := handleClaimFundsFor(t, svc, NewTestNip47Controller(svc), wallet, nipcash.CashRedeemRequest{
		Invoice:       tests.MockZeroAmountInvoice,
		Amount:        &full, // same-node => fee waived => the FULL amount is required
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: claimantPubkey,
		IdentityEvent: mustMarshal(t, proof),
	})
	require.Nil(t, response.Error)

	// A same-node payment is intercepted before any real routing, so the node's
	// payment RPC should not be reached at all. If a future change does route it,
	// the assertion below is the one that must hold.
	for _, call := range mockLn.SendPaymentSyncCalls() {
		assert.Nil(t, call.FeeLimitMloki,
			"a route cap was imposed on a fee-free same-node redemption; a cap of zero "+
				"means \"no fee permitted\" to LND, not \"no limit\"")
	}
}
