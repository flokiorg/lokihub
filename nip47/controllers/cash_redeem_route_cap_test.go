package controllers

// The fee-shape fix (`f6e0124`) rests on one invariant, asserted in its own commit
// message: **payout + route_cap == the slice**. The Hub does not pay for routing; it
// withholds enough to cover it and caps the route at what it withheld.
//
// Until now nothing checked that the cap ever reached the node. The mock's
// SendPaymentSync discarded all three of its arguments and returned a fixed preimage,
// so the whole guarantee was asserted at the arithmetic layer and nowhere else — a
// regression that dropped `routeCap` on the floor, or passed the gross amount, or
// passed a cap on a fee-free same-node redeem, would have left every test green while
// the Hub went back to paying for other people's routes out of its own balance.
//
// This is the test that gap was hiding. It reads tests.MockLn's recordings
// (SendPaymentSyncCalls), added for exactly this.

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
			name:       "proportional_fee_caps_the_route_at_what_was_withheld",
			sliceMloki: 1000, feePpm: 100_000, // 10% => fee 100
			wantPayout: 900, wantRouteCap: ptrUint64(100),
			why: "the invariant: 900 paid out + 100 route cap == the 1000 slice",
		},
		{
			name:       "flat_base_caps_the_route_too",
			sliceMloki: 10_000, feeBaseMloki: 500,
			wantPayout: 9500, wantRouteCap: ptrUint64(500),
			why: "the base is the half of f6e0124 that makes small slices pay for themselves",
		},
		{
			name:       "base_plus_proportional_cap_is_their_sum",
			sliceMloki: 10_000, feeBaseMloki: 500, feePpm: 100_000, // 500 + 1000
			wantPayout: 8500, wantRouteCap: ptrUint64(1500),
			why: "both components are withheld, so both are available to the route",
		},
		{
			name:       "no_fee_configured_passes_NO_cap",
			sliceMloki: 1000,
			wantPayout: 1000, wantRouteCap: nil,
			why: "zero is not a cap: a Hub that withheld nothing made no promise, so it " +
				"keeps the old reserve rather than failing every external redeem",
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
				"the route cap is not the fee that was withheld — %s", c.why)

			// The invariant itself, stated as arithmetic rather than inferred from the
			// two assertions above.
			//nolint:gosec // every row's sliceMloki is a positive literal
			assert.Equal(t, uint64(c.sliceMloki), *calls[0].Amount+*calls[0].FeeLimitMloki,
				"payout + route_cap != slice: the Hub either eats the difference or "+
					"pockets it, and f6e0124 exists so that it does neither")
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
