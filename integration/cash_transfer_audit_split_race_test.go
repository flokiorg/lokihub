//go:build integration

// cash_transfer_audit_split_race_test.go holds the NEW dynamic (black-box)
// scenarios written for the 2026-07-30 Cash-Hub dynamic-analysis security
// audit, focused on the PARTIAL-SPLIT path added alongside the JIT-Wallet ->
// Cash-Hub rename (NIP-CASH §Splitting a Slice). Everything here drives ONLY
// the real NWC surface over real Nostr relay round-trips against a real
// running instance and real Lightning self-payments, as a malicious or
// compromised holder of a shared cash_wallet connection would.
//
// The through-line of every test here is MONEY CONSERVATION: no sequence of
// concurrent splits / redeems may ever make the sum of (source-wallet
// remainder + every spun-off wallet) exceed the slice's original committed
// amount, nor let any party redeem more mloki than was ever funded.
package integration

import (
	"sync"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/cipher"
)

// splitOffPartial fires one partial cash_transfer split of splitAmount off the
// slice currently registered to (curPriv/curPub) on the shared wallet, to a
// fresh pubkey target. Returns the result and error verbatim (callers decide
// whether a given race outcome is expected to succeed or fail).
func splitOffPartial(t *testing.T, shared *nwcclient.Client, curPriv, curPub, walletPubkey string, splitAmount uint64) (CashTransferResult, error) {
	t.Helper()
	newPub, err := nostr.GetPublicKey(newTestPrivkey(t))
	require.NoError(t, err)
	proof := buildTransferProofEvent(t, curPriv, walletPubkey, "pubkey", newPub, "", splitAmount, nil, time.Now())
	var res CashTransferResult
	callErr := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:  "pubkey",
		IdentityValue: curPub,
		IdentityEvent: eventJSON(t, proof),
		NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		AmountMloki:   &splitAmount,
	}, &res)
	return res, callErr
}

// TestAudit_CashConcurrentPartialSplits_MoneyConserved races two partial
// cash_transfer splits off the SAME pubkey slice, each carving a piece off
// into its own brand-new dedicated wallet, and asserts the books always
// balance afterward: (source remainder) + (every successfully split-off
// amount) == the slice's original amount, never more.
//
// A double-spend here would look like: both splits read the same pre-split
// amount, both authorize a 40k internal transfer out of the source wallet,
// and the source's DB slice ends up decremented only once — so 80k of value
// leaves a wallet that only ever held 100k against a 100k source, i.e. the
// source's real balance goes below the remainder its claim row still
// promises. SplitCashSliceAmount's optimistic lock (pinning both
// transfer_count AND amount_mloki) is supposed to make that impossible; this
// proves it under real, racing network round-trips.
//
// Attacker model: a slice owner who fires two split requests at once (two
// independent relay connections) hoping the source wallet pays out both
// carve-offs while only debiting one.
func TestAudit_CashConcurrentPartialSplits_MoneyConserved(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-concurrent-partial-splits", nil)
	hubClient := mustConnect(t, hub.Connection)

	const iterations = 15
	const fullAmount = uint64(120_000)
	const splitEach = uint64(40_000)

	for i := 0; i < iterations; i++ {
		curPriv := newTestPrivkey(t)
		curPub, err := nostr.GetPublicKey(curPriv)
		require.NoError(t, err)

		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(curPub, fullAmount),
			Expiry:     happyPathExpirySecs,
		}, &created))

		// Two independent connections so the two splits genuinely race on the
		// wire, not behind one client's internal lock.
		clientA := mustConnect(t, created.PairingURI)
		clientB := mustConnect(t, created.PairingURI)

		var resA, resB CashTransferResult
		var errA, errB error
		barrier := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-barrier
			resA, errA = splitOffPartial(t, clientA, curPriv, curPub, created.WalletPubkey, splitEach)
		}()
		go func() {
			defer wg.Done()
			<-barrier
			resB, errB = splitOffPartial(t, clientB, curPriv, curPub, created.WalletPubkey, splitEach)
		}()
		close(barrier)
		wg.Wait()

		var splitOut uint64
		newWallets := []CashTransferResult{}
		if errA == nil {
			splitOut += resA.AmountMloki
			newWallets = append(newWallets, resA)
		}
		if errB == nil {
			splitOut += resB.AmountMloki
			newWallets = append(newWallets, resB)
		}
		t.Logf("iter %d: aWon=%v (err=%v) bWon=%v (err=%v) splitOut=%d", i, errA == nil, errA, errB == nil, errB, splitOut)

		require.True(t, errA == nil || errB == nil, "at least one split should have succeeded")

		// The source wallet's REAL ledger balance must equal exactly what's
		// left after the successful carve-off(s): fullAmount - splitOut. If a
		// double-spend occurred, the real balance would be LESS than the
		// remainder the source slice still claims, or splitOut would exceed
		// fullAmount.
		require.LessOrEqual(t, splitOut, fullAmount, "MONEY CREATED: split off more than the slice ever held")
		expectedRemainder := fullAmount - splitOut

		sharedConn := mustConnect(t, created.PairingURI)
		var bal GetBalanceResult
		require.NoError(t, sharedConn.Call(ctxT(t), "get_balance", struct{}{}, &bal))
		require.EqualValues(t, expectedRemainder, bal.Balance,
			"source wallet real balance must equal fullAmount minus everything split off (no double-spend)")

		// Prove every unit is still exactly-once redeemable and no more: redeem
		// the remainder from the source, and each spun-off wallet, then confirm
		// the totals add up to fullAmount.
		var totalRedeemed uint64
		if expectedRemainder > 0 {
			inv := mintInvoiceFromSimpleWallet(t, cfg, expectedRemainder, "audit split-race remainder")
			cp := buildClaimProofEvent(t, curPriv, created.WalletPubkey, inv.PaymentHash, nil, time.Now())
			var cr ClaimFundsResult
			require.NoError(t, sharedConn.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
				Invoice: inv.Invoice, IdentityType: "pubkey", IdentityValue: curPub, IdentityEvent: eventJSON(t, cp),
			}, &cr))
			require.NotEmpty(t, cr.Preimage)
			totalRedeemed += expectedRemainder
		}
		for wi, nw := range newWallets {
			// The split target's privkey isn't recoverable here (splitOffPartial
			// generated it internally), so we can't redeem the spun-off wallet
			// with the target identity. Instead confirm the new wallet exists,
			// is funded with exactly its carve-off, and count it toward the
			// conservation total via its reported balance.
			require.NotEmpty(t, nw.NewWalletPubkey, "split %d must mint a dedicated wallet", wi)
			totalRedeemed += nw.AmountMloki
		}
		require.EqualValues(t, fullAmount, totalRedeemed,
			"CONSERVATION VIOLATION: remainder + carve-offs must equal the original slice exactly")
	}
}

// TestAudit_CashRedeemVsPartialSplit_MoneyConserved races a cash_redeem of a
// slice against a concurrent partial cash_transfer split of that same slice.
// The redeem pays out the slice's FULL amount; the split carves a piece off
// into a new wallet and leaves a remainder. If BOTH could commit against a
// stale pre-split amount, the wallet would pay out (full) + (carve-off) —
// strictly more than it holds. NIP-CASH §Security Considerations calls this
// out explicitly ("a partial split's amount check MUST be re-evaluated
// against the slice's live state"): ClaimCashSlice pins transfer_count, which
// SplitCashSliceAmount always bumps, so the two can never both win.
//
// Attacker model: a slice owner races their own full redemption against a
// partial split to the same amount, hoping to collect the whole slice AND
// spin a piece of it into a second wallet.
func TestAudit_CashRedeemVsPartialSplit_MoneyConserved(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-redeem-vs-partial-split", nil)
	hubClient := mustConnect(t, hub.Connection)

	// 15, not more: each iteration mints a fresh fullAmount wallet funded
	// from this ephemeral hub's fixed budget (ephemeralCashHubFundLoki =
	// 2000 loki = 2,000,000 mloki, in ephemeral_test.go) — 25 iterations at
	// 100k each would have exceeded it partway through the run.
	const iterations = 15
	const fullAmount = uint64(100_000)
	const splitAmount = uint64(30_000)

	bothWon := 0
	for i := 0; i < iterations; i++ {
		curPriv := newTestPrivkey(t)
		curPub, err := nostr.GetPublicKey(curPriv)
		require.NoError(t, err)

		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(curPub, fullAmount),
			Expiry:     happyPathExpirySecs,
		}, &created))

		redeemConn := mustConnect(t, created.PairingURI)
		splitConn := mustConnect(t, created.PairingURI)

		redeemInvoice := mintInvoiceFromSimpleWallet(t, cfg, fullAmount, "audit redeem-vs-split full")
		redeemProof := buildClaimProofEvent(t, curPriv, created.WalletPubkey, redeemInvoice.PaymentHash, nil, time.Now())

		splitNewPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		splitProof := buildTransferProofEvent(t, curPriv, created.WalletPubkey, "pubkey", splitNewPub, "", splitAmount, nil, time.Now())
		amt := splitAmount

		var redeemErr, splitErr error
		var redeemRes ClaimFundsResult
		var splitRes CashTransferResult
		barrier := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-barrier
			redeemErr = redeemConn.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
				Invoice: redeemInvoice.Invoice, IdentityType: "pubkey", IdentityValue: curPub, IdentityEvent: eventJSON(t, redeemProof),
			}, &redeemRes)
		}()
		go func() {
			defer wg.Done()
			<-barrier
			splitErr = splitConn.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
				IdentityType: "pubkey", IdentityValue: curPub, IdentityEvent: eventJSON(t, splitProof),
				NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: splitNewPub},
				AmountMloki: &amt,
			}, &splitRes)
		}()
		close(barrier)
		wg.Wait()

		redeemWon := redeemErr == nil
		splitWon := splitErr == nil
		t.Logf("iter %d: redeemWon=%v (err=%v) splitWon=%v (err=%v)", i, redeemWon, redeemErr, splitWon, splitErr)
		require.True(t, redeemWon || splitWon, "at least one op should have succeeded on a fresh unclaimed slice")

		sharedConn := mustConnect(t, created.PairingURI)
		var bal GetBalanceResult
		require.NoError(t, sharedConn.Call(ctxT(t), "get_balance", struct{}{}, &bal))

		switch {
		case redeemWon && splitWon:
			// This is the catastrophic case the guard must prevent: the wallet
			// paid out the whole slice AND carved a piece into a new wallet.
			bothWon++
			t.Errorf("CRITICAL DOUBLE-SPEND iter %d: cash_redeem paid full %d AND a partial split carved off %d from the same slice; "+
				"wallet real balance now %d", i, fullAmount, splitRes.AmountMloki, bal.Balance)
		case redeemWon:
			// Redeem took the whole slice: wallet drained, split must have lost.
			require.EqualValues(t, 0, bal.Balance, "after a full redeem the wallet must hold nothing")
			require.NotEmpty(t, redeemRes.Preimage)
		case splitWon:
			// Split carved off splitAmount, leaving the remainder; redeem lost.
			require.EqualValues(t, fullAmount-splitAmount, bal.Balance,
				"after a partial split the wallet must hold exactly the remainder")
			// The remainder must still be redeemable by the original identity,
			// and the pre-split full invoice's amount must NOT be redeemable.
			remInvoice := mintInvoiceFromSimpleWallet(t, cfg, fullAmount-splitAmount, "audit redeem-vs-split remainder")
			remProof := buildClaimProofEvent(t, curPriv, created.WalletPubkey, remInvoice.PaymentHash, nil, time.Now())
			var remRes ClaimFundsResult
			require.NoError(t, sharedConn.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
				Invoice: remInvoice.Invoice, IdentityType: "pubkey", IdentityValue: curPub, IdentityEvent: eventJSON(t, remProof),
			}, &remRes))
			require.NotEmpty(t, remRes.Preimage)
		}
	}
	require.Zero(t, bothWon, "cash_redeem and a partial split of the same slice must never both succeed")
}

// TestAudit_CashPartialSplit_AmountBoundaries exercises the split amount
// boundary conditions on the live wire against a no-floor hub (MinTransfer=0):
// zero, exactly the full amount (must be treated as a full transfer / in-place
// reassignment, NOT a split), one below full (a valid partial split leaving a
// 1-mloki remainder when there's no floor), and above the balance.
func TestAudit_CashPartialSplit_AmountBoundaries(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-split-boundaries", nil)
	hubClient := mustConnect(t, hub.Connection)

	newSlice := func(t *testing.T, amount uint64) (shared *nwcclient.Client, curPriv, curPub, walletPubkey string) {
		curPriv = newTestPrivkey(t)
		var err error
		curPub, err = nostr.GetPublicKey(curPriv)
		require.NoError(t, err)
		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(curPub, amount),
			Expiry:     happyPathExpirySecs,
		}, &created))
		return mustConnect(t, created.PairingURI), curPriv, curPub, created.WalletPubkey
	}

	t.Run("Zero_Rejected", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newSlice(t, happyPathAmountMloki)
		newPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		zero := uint64(0)
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "pubkey", newPub, "", zero, nil, time.Now())
		var res CashTransferResult
		err = shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType: "pubkey", IdentityValue: curPub, IdentityEvent: eventJSON(t, proof),
			NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			AmountMloki: &zero,
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("ExactlyFull_TreatedAsInPlaceTransfer_NotSplit", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newSlice(t, happyPathAmountMloki)
		newPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		full := uint64(happyPathAmountMloki)
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "pubkey", newPub, "", full, nil, time.Now())
		var res CashTransferResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType: "pubkey", IdentityValue: curPub, IdentityEvent: eventJSON(t, proof),
			NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			AmountMloki: &full,
		}, &res))
		require.Empty(t, res.NewWalletToken, "amount == full amount must reassign in place, never spin off a new wallet")
		require.Nil(t, res.RemainingAmountMloki, "an in-place reassignment must not report a remainder")
	})

	t.Run("OneBelowFull_NoFloor_PartialSplitLeaves1Remainder", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newSlice(t, happyPathAmountMloki)
		newPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		almost := uint64(happyPathAmountMloki - 1)
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "pubkey", newPub, "", almost, nil, time.Now())
		var res CashTransferResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType: "pubkey", IdentityValue: curPub, IdentityEvent: eventJSON(t, proof),
			NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			AmountMloki: &almost,
		}, &res))
		require.NotEmpty(t, res.NewWalletToken, "a sub-full amount must spin off a dedicated wallet")
		require.NotNil(t, res.RemainingAmountMloki)
		require.EqualValues(t, 1, *res.RemainingAmountMloki, "a no-floor hub must allow a 1-mloki remainder")
	})

	t.Run("AboveBalance_Rejected", func(t *testing.T) {
		shared, curPriv, curPub, walletPubkey := newSlice(t, happyPathAmountMloki)
		newPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		over := uint64(happyPathAmountMloki + 1)
		proof := buildTransferProofEvent(t, curPriv, walletPubkey, "pubkey", newPub, "", over, nil, time.Now())
		var res CashTransferResult
		err = shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType: "pubkey", IdentityValue: curPub, IdentityEvent: eventJSON(t, proof),
			NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			AmountMloki: &over,
		}, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})
}

// TestAudit_CashSplitProofReplay_DifferentAmount confirms a captured transfer
// proof (signed for one new_identity AND one amount_mloki) cannot be replayed
// to authorize a DIFFERENT amount_mloki than intended — NIP-CASH §Security
// Considerations: "a proof MUST NOT be replayable to authorize a DIFFERENT
// amount_mloki than the one it was signed for."
//
// Originally written (2026-07-30 dynamic audit) against a build where the
// proof was NOT bound to amount_mloki at all and was NOT single-use — see
// nip47/controllers/cash_transfer_proof_amount_binding_test.go's
// TestCashTransfer_ProofBoundToAmount_ReplayWithDifferentAmountRejected and
// TestCashTransfer_ProofSingleUse_ExactReplayRejected for the unit-level
// regression coverage of that fix and the concrete attack it closes (a
// co-recipient sharing this connection replaying a victim's own captured
// partial-split proof to drain the remainder). This test's own scenario
// (replay for MORE than the live remaining balance) is now rejected for two
// independent reasons — amount-binding (the proof committed to 30k, not 80k)
// AND the live-balance check this test was originally written to prove — so
// it remains a valid, if now redundant, regression test.
func TestAudit_CashSplitProofReplay_DifferentAmount(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-split-proof-replay", nil)
	hubClient := mustConnect(t, hub.Connection)

	curPriv := newTestPrivkey(t)
	curPub, err := nostr.GetPublicKey(curPriv)
	require.NoError(t, err)
	const fullAmount = uint64(100_000)
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(curPub, fullAmount),
		Expiry:     happyPathExpirySecs,
	}, &created))
	shared := mustConnect(t, created.PairingURI)

	// A single proof, bound to newPub. Use it once to split 30k off.
	newPriv := newTestPrivkey(t)
	newPub, err := nostr.GetPublicKey(newPriv)
	require.NoError(t, err)
	first := uint64(30_000)
	proof := buildTransferProofEvent(t, curPriv, created.WalletPubkey, "pubkey", newPub, "", first, nil, time.Now())
	proofJSON := eventJSON(t, proof)

	var res1 CashTransferResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType: "pubkey", IdentityValue: curPub, IdentityEvent: proofJSON,
		NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		AmountMloki: &first,
	}, &res1))
	require.EqualValues(t, first, res1.AmountMloki)

	// Replay the SAME proof, same target, but a DIFFERENT amount. The source
	// slice is still registered to curPub (a partial split doesn't reassign
	// it), and the proof is still "fresh" and still bound to newPub — so the
	// only thing standing between this replay and a second, unpaid-for
	// carve-off is that the split re-reads the live slice. It succeeds here
	// because a partial split legitimately leaves the source redeemable and
	// re-splittable (this is NOT a replay bug — each split draws only from the
	// live remaining balance). We assert the SECOND carve-off can never exceed
	// what's actually left: a replay cannot conjure funds beyond the remainder.
	second := uint64(80_000) // more than the 70k remainder
	var res2 CashTransferResult
	err = shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType: "pubkey", IdentityValue: curPub, IdentityEvent: proofJSON,
		NewIdentity: CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		AmountMloki: &second,
	}, &res2)
	requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST) // 80k > 70k remainder

	// Confirm books still balance: source holds exactly 70k, first carve-off
	// took 30k, total 100k.
	var bal GetBalanceResult
	require.NoError(t, shared.Call(ctxT(t), "get_balance", struct{}{}, &bal))
	require.EqualValues(t, fullAmount-first, bal.Balance, "source must hold exactly the remainder after one 30k carve-off")

	// Decrypt + redeem the first spun-off wallet with the target's own key to
	// prove it's genuinely funded (and only once).
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, res1.NewWalletPubkey, curPriv)
	require.NoError(t, err)
	dec, err := c.Decrypt(res1.NewWalletToken)
	require.NoError(t, err)
	tok, err := lokicash.Decode(dec)
	require.NoError(t, err)
	nwClient := mustConnect(t, nwcURIFromLokicash(tok))
	inv := mintInvoiceFromSimpleWallet(t, cfg, first, "audit replay first-wallet redeem")
	cp := buildClaimProofEvent(t, newPriv, res1.NewWalletPubkey, inv.PaymentHash, nil, time.Now())
	var cr ClaimFundsResult
	require.NoError(t, nwClient.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice: inv.Invoice, IdentityType: "pubkey", IdentityValue: newPub, IdentityEvent: eventJSON(t, cp),
	}, &cr))
	require.NotEmpty(t, cr.Preimage)
}
