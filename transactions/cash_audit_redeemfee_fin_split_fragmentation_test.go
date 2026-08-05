package transactions

// Financial/economic design review (2026-08-02) — Cash Hub redeem fee
// (redeem_fee_ppm) vs. cash_transfer/Split fragmentation, the same-node
// exemption's real-world reach, and concurrent-redemption safety.
//
// This file is a companion to cash_audit_fin_redeem_fee_test.go (the
// original shared-pool stranding finding) and
// cash_redeem_fee_reconciliation_test.go (the fix's basic delta orderings).
// It targets exactly the questions the redeem-fee fix's own review brief
// raised as NOT yet covered by either of those: does the per-redemption
// fairness invariant survive splitting, concurrency, and the ppm-floor's
// rounding-to-zero behavior — and is the same-node exemption something more
// than a hypothetical.
//
// FINDING (Low/Informational — a revenue-optimization gap, not a fund-safety
// bug): cash_transfer/Split is explicitly, deliberately fee-free (NIP-CASH
// §The Redeem Fee: "a transfer or split fee MUST NOT exist"), and each
// resulting child slice inherits its parent's RedeemFeePpm UNCHANGED
// (cashwallet.Split's SplitParams.RedeemFeePpm — see create.go's Split, and
// apps/cash_hub_service.go's SplitCashSliceAmount). CalculateFeeSkimMloki
// (transactions_service.go) floors, never rounds up
// (floor(amount*ppm/1e6)). Because floor is subadditive
// (floor(a)+floor(b) <= floor(a+b) for any nonnegative a, b), splitting a
// slice into N pieces before redeeming each separately can only ever
// collect LESS aggregate hub fee than redeeming the same total value in one
// shot — and, whenever every resulting piece's own amount*ppm/1e6 rounds
// below 1 mloki, the aggregate fee collected is exactly ZERO, regardless of
// how large the original slice or how high the configured ppm rate is. This
// costs the recipient nothing: splitting itself is fee-free by design, and
// nothing in the protocol limits how many times a slice may be split
// (only min_transfer_mloki bounds how SMALL a piece may be — and its
// documented default is 0, "no floor", independent of whatever
// redeem_fee_ppm the same Hub configures). This doesn't strand any other
// recipient's slice and doesn't let anyone collect more than their own
// value — the core fairness invariant this feature exists to protect still
// holds exactly (see TestCashAuditRedeemFeeFin_ConcurrentRedemptions_
// InvariantHoldsUnderInterleaving below) — but it does mean redeem_fee_ppm
// is a soft, gameable price signal, not a hard floor on what the Hub
// actually recovers: a recipient who fragments finely enough (or a
// mildly-technical recipient's wallet software that always splits into
// many small pieces before redeeming, for whatever unrelated reason — e.g.
// privacy) pays strictly less aggregate fee than one who redeems in a
// single shot, and the Hub has no visibility into, or defense against,
// this beyond raising min_transfer_mloki (which the fee mechanism does not
// require, or even suggest, an operator set).

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/db/queries"
	decodepay "github.com/flokiorg/lokihub/decodepay"
	"github.com/flokiorg/lokihub/tests"
)

// TestCashAuditRedeemFeeFin_SplitFragmentation_ZeroesFeeMath is a pure,
// DB-free proof of the arithmetic underlying the finding above, exercised
// directly against CalculateFeeSkimMloki — the exact function
// cash_redeem_controller.go step 9 and list_recipients_controller.go both
// call.
func TestCashAuditRedeemFeeFin_SplitFragmentation_ZeroesFeeMath(t *testing.T) {
	const ppm = 100_000 // 10% — a plausible real redeem_fee_ppm

	// An 18 mloki slice redeemed whole owes a real, nonzero fee:
	// floor(18 * 100000 / 1e6) = floor(1.8) = 1 mloki.
	lumpFee := CalculateFeeSkimMloki(18, ppm)
	require.Equal(t, uint64(1), lumpFee)

	// The SAME 18 mloki, split (fee-free, per NIP-CASH §The Redeem Fee) into
	// two 9-mloki child slices — each inheriting the identical RedeemFeePpm
	// unchanged — and each later redeemed independently:
	fragmentFeeA := CalculateFeeSkimMloki(9, ppm)
	fragmentFeeB := CalculateFeeSkimMloki(9, ppm)
	assert.Zero(t, fragmentFeeA, "9 * 10%% = 0.9, floors to 0")
	assert.Zero(t, fragmentFeeB, "9 * 10%% = 0.9, floors to 0")

	totalFragmentedFee := fragmentFeeA + fragmentFeeB
	assert.Less(t, totalFragmentedFee, lumpFee,
		"splitting the slice in two, then redeeming each half, strictly reduces the aggregate hub fee vs. one lump redemption of the identical total value")
	assert.Zero(t, totalFragmentedFee,
		"in this case the fee isn't just reduced, it's eliminated entirely — a 10%% configured rate recovers 0%% in practice")

	// This isn't a cherry-picked edge case: floor(a) + floor(b) <=
	// floor(a+b) for any nonnegative a, b, so fragmenting NEVER increases,
	// and routinely strictly decreases, the aggregate quoted fee, for any
	// ppm and any split point. Sweep a handful of unrelated
	// total/ppm/piece-count combinations to confirm the direction of the
	// inequality always holds, not just in the hand-picked case above.
	for _, tc := range []struct {
		name      string
		total     uint64
		ppm       int
		numPieces uint64
	}{
		{name: "small ppm, many pieces", total: 1_000, ppm: 1_000, numPieces: 7},
		{name: "the 18/9+9 case restated as 95/11", total: 95, ppm: 100_000, numPieces: 11},
		{name: "tiny ppm, large amount", total: 999_999, ppm: 1, numPieces: 3},
		{name: "large ppm, many tiny pieces", total: 50_000, ppm: 500_000, numPieces: 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lump := CalculateFeeSkimMloki(tc.total, tc.ppm)
			piece := tc.total / tc.numPieces
			remainder := tc.total - piece*(tc.numPieces-1)
			var fragmented uint64
			for i := uint64(0); i < tc.numPieces-1; i++ {
				fragmented += CalculateFeeSkimMloki(piece, tc.ppm)
			}
			fragmented += CalculateFeeSkimMloki(remainder, tc.ppm)
			assert.LessOrEqual(t, fragmented, lump,
				"fragmenting into %d pieces must never collect MORE aggregate fee than one lump redemption of the same %d-mloki total at %d ppm",
				tc.numPieces, tc.total, tc.ppm)
		})
	}
}

// TestCashAuditRedeemFeeFin_DustFragmentRedemption_RealEndToEnd_ZeroFeeCollected
// replays one concrete fragment (the "9" half of the 18-mloki example above)
// through the REAL settlement path — SendPaymentSync ->
// markTransactionSettled -> reconcileCashRedeemFee, the exact machinery
// cash_redeem_controller.go's step 10 and step 4 of NIP-CASH's own
// processing algorithm invoke — not just the pure function in isolation.
// This confirms the finding isn't an artifact of testing
// CalculateFeeSkimMloki out of context: a genuinely tiny, genuinely
// external redemption really does settle with the Hub collecting nothing,
// via the real DB transaction and the real isolated-balance accounting.
func TestCashAuditRedeemFeeFin_DustFragmentRedemption_RealEndToEnd_ZeroFeeCollected(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	const ppm = 100_000           // 10% redeem_fee_ppm — e.g. configured to recover real routing costs
	const originalSlice = uint64(18) // what the Hub would have quoted/collected on ONE lump redemption
	const fragmentAmount = uint64(9) // half of it, split off via a (fee-free) cash_transfer

	lumpFee := CalculateFeeSkimMloki(originalSlice, ppm)
	require.Equal(t, uint64(1), lumpFee, "the un-fragmented slice would have owed a real, nonzero fee")

	fragmentFee := CalculateFeeSkimMloki(fragmentAmount, ppm)
	require.Zero(t, fragmentFee, "the SAME ppm, applied to half the value, floors to zero")

	hub, wallet := newCashHubAndWallet(t, svc, fragmentAmount)

	net := fragmentAmount - fragmentFee // == fragmentAmount: the quoted fee is 0
	txn, err := NewTransactionsService(svc.DB, svc.EventPublisher).SendPaymentSync(
		tests.MockZeroAmountInvoice, &net, redeemMetadata(fragmentFee),
		svc.LNClient, &wallet.ID, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, txn)

	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, wallet.ID),
		"the fragment's own dedicated wallet still drains exactly its own (tiny) committed amount — no stranding, no overpay")
	assert.Zero(t, queries.GetIsolatedBalance(svc.DB, hub.ID),
		"the Hub collects NOTHING on this redemption: a nonzero configured rate rounds down to a zero real fee for a small enough piece")

	var reconciliationRows int64
	svc.DB.Model(&db.Transaction{}).Where("app_id = ?", hub.ID).Count(&reconciliationRows)
	assert.Zero(t, reconciliationRows,
		"delta == 0 creates no reconciliation rows at all — this fee-avoidance leaves no ledger trace distinguishing it from an ordinary same-node redemption")
}

// TestCashAuditRedeemFeeFin_SameNodeExemption_AnyColocatedAppKindQualifies
// answers the review brief's fairness question directly: "could a Hub owner
// or a recipient dodge the fee by routing through an intermediate same-node
// hop — is that even possible given the codebase's actual infrastructure,
// or purely theoretical?"
//
// transactions.IsSelfPayment (the SAME predicate cash_redeem_controller.go
// step 9 and SendPaymentSync's own self-payment interception both call) has
// no app-kind awareness at all: it only checks the invoice's payee pubkey
// against this node's own pubkey, and that SOME incoming transaction row
// exists for that payment hash — any app, any kind, any purpose. This test
// proves that directly: an invoice minted by a completely unrelated
// AppKindIsolated ("Simple Subwallet") app — nothing to do with any
// cash_hub or this redemption's own recipient roster — is enough to zero
// out what would otherwise be a real, sizeable fee.
//
// Whether this is exploitable in practice depends on deployment shape, not
// on this predicate: api.CreateApp (http_service.go's fullAccessApiGroup)
// requires the node owner's own admin credentials, so an arbitrary
// stranger cannot self-provision a landing wallet on someone else's
// instance. But Lokihub is fundamentally a general-purpose NWC hub, not a
// cash-only appliance — an operator who ALSO issues ordinary personal
// wallets/connections to the same people who happen to be Cash Hub
// recipients (a very ordinary shape: a family, a team, a community running
// one node for everyone) hands exactly this same-node landing spot to
// every such person, for free, by construction. So this is not purely
// theoretical, but it IS narrower than "any recipient, on any deployment" —
// it requires the recipient to already hold some other account on the same
// physical node.
func TestCashAuditRedeemFeeFin_SameNodeExemption_AnyColocatedAppKindQualifies(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	svc.LNClient.(*tests.MockLn).Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"

	// A completely unrelated, ordinary personal sub-wallet — an
	// AppKindIsolated ("Simple Subwallet") — happens to be the one that
	// minted the invoice being presented for cash_redeem. It was never
	// created for, or by, the Cash Hub or any of its recipients.
	landingApp := &db.App{Name: "unrelated-personal-wallet", AppPubkey: auditRandHex32(), Kind: db.AppKindIsolated}
	require.NoError(t, svc.DB.Create(landingApp).Error)
	mockPreimage := "colocated-landing-preimage"
	require.NoError(t, svc.DB.Create(&db.Transaction{
		AppId:          &landingApp.ID,
		State:          constants.TRANSACTION_STATE_PENDING,
		Type:           constants.TRANSACTION_TYPE_INCOMING,
		PaymentRequest: tests.MockInvoice,
		PaymentHash:    tests.MockPaymentHash,
		Preimage:       &mockPreimage,
		AmountMloki:    123_000,
	}).Error)

	paymentRequest, err := decodepay.Decode(tests.MockInvoice)
	require.NoError(t, err)

	// This is exactly cash_redeem_controller.go step 9's own call.
	willBeSelfPayment := IsSelfPayment(svc.DB, paymentRequest, svc.LNClient)
	require.True(t, willBeSelfPayment,
		"IsSelfPayment doesn't check WHICH app minted the matching incoming transaction, or why — any co-located app kind qualifies")

	const ppm = 100_000 // 10% — what this redemption would have owed as a genuine external payment
	hubFeeMloki := uint64(0)
	if !willBeSelfPayment {
		hubFeeMloki = CalculateFeeSkimMloki(123_000, ppm)
	}
	assert.Zero(t, hubFeeMloki,
		"redirecting the redemption invoice through ANY co-located account — cash-related or not — forgoes the entire fee (12,300 mloki here) that a genuinely external redemption of the same amount would have owed")
}

// TestCashAuditRedeemFeeFin_ConcurrentRedemptions_InvariantHoldsUnderInterleaving
// stress-tests the fairness invariant under real concurrency, not just
// sequential test cases: two DIFFERENT recipients on the SAME shared
// cash_wallet redeem at (as close as the test harness allows) the same
// real-world moment, each with their own quoted fee and their own real
// routing cost, racing each other's settlement/reconciliation. The claimed
// invariant (NIP-CASH §The Redeem Fee) is that the wallet's total debit for
// EACH redemption is exactly that slice's own claimed amount, regardless of
// ordering or interleaving — so the two together must drain the wallet by
// exactly claimedA + claimedB, no more, no less, and the Hub must net
// exactly (feeA - realA) + (feeB - realB), regardless of which of the two
// commits first.
//
// The test DB's sqlite connection sets _pragma=busy_timeout(5000) (see
// db/db.go), so concurrent writers block-and-retry rather than erroring —
// this test can therefore run real goroutines against the same DB without
// flaking, and any bug that read a shared balance/lookup OUTSIDE the
// per-payment atomic transaction (rather than fully re-deriving it inside
// markTransactionSettled's own tx, as reconcileCashRedeemFee's doc comment
// requires) would show up here as a wrong final total, not just a
// hypothetical.
func TestCashAuditRedeemFeeFin_ConcurrentRedemptions_InvariantHoldsUnderInterleaving(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	// Recipient A: redeems via the flexible zero-amount invoice.
	const claimedA = uint64(50_000)
	const feeA = uint64(5_000) // 10% of claimedA
	netA := uint64(claimedA - feeA)

	// Recipient B: redeems via the fixed-amount canned invoice (net pinned
	// at 123000), so claimedB/feeB are solved backwards from that.
	const claimedB = uint64(136_666)
	const feeB = uint64(13_666) // floor(136666 * 0.1) = 13666
	const netB = claimedB - feeB
	require.Equal(t, uint64(123_000), netB, "netB must land exactly on tests.MockInvoice's own fixed amount")

	funded := claimedA + claimedB
	hub, wallet := newCashHubAndWallet(t, svc, funded)

	txnSvc := NewTransactionsService(svc.DB, svc.EventPublisher)

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errA = txnSvc.SendPaymentSync(
			tests.MockZeroAmountInvoice, &netA, redeemMetadata(feeA),
			svc.LNClient, &wallet.ID, nil,
		)
	}()
	go func() {
		defer wg.Done()
		_, errB = txnSvc.SendPaymentSync(
			tests.MockInvoice, nil, redeemMetadata(feeB),
			svc.LNClient, &wallet.ID, nil,
		)
	}()
	wg.Wait()

	require.NoError(t, errA, "recipient A's redemption must succeed regardless of how it interleaves with B's")
	require.NoError(t, errB, "recipient B's redemption must succeed regardless of how it interleaves with A's")

	// Both real routing fees default to 0 (MockLn's zero-value response), so
	// the Hub should net exactly feeA+feeB, and the wallet should be left at
	// exactly zero — the combined claimed total, no more and no less,
	// exactly as the sequential-case tests in
	// cash_redeem_fee_reconciliation_test.go already establish one
	// redemption at a time.
	assert.Equal(t, int64(0), queries.GetIsolatedBalance(svc.DB, wallet.ID),
		"two concurrent redemptions against the same shared wallet must still drain it by EXACTLY the sum of their own claimed amounts")
	assert.Equal(t, int64(feeA+feeB), queries.GetIsolatedBalance(svc.DB, hub.ID),
		"the Hub's net revenue must be exactly the sum of each redemption's own (fee-real) delta, unaffected by the race")
}
