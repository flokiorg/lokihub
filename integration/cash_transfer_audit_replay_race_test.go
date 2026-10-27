//go:build integration

// cash_transfer_audit_replay_race_test.go covers the single-use replay guard
// (db.CashTransferProof)
// under real, racing network round-trips, plus a live probe of the
// cash_transfer rate limit. The existing money-conservation race test
// (cash_transfer_audit_split_race_test.go) fires DISTINCT proofs each round; it
// never exercises what happens when the SAME captured proof is submitted twice
// — which is precisely the co-recipient replay the guard exists to stop.
package integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

// TestAudit_CashTransferIdenticalProofRace_ExactlyOneWins fires the EXACT same
// captured transfer proof (same wallet, same target, same amount, same event
// id) from two independent connections at once. The single-use replay guard
// must let exactly one through: a co-recipient who captured a victim's proof
// off the shared connection must not be able to race a duplicate of it to
// double the carve-off. Books must balance: only one 30k piece may ever leave a
// 100k slice this way.
func TestAudit_CashTransferIdenticalProofRace_ExactlyOneWins(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubAppID, admin := createEphemeralCashHub(t, cfg, "audit-identical-proof-race", nil)
	hubClient := mustConnect(t, hub.Connection)

	const iterations = 12
	const fullAmount = uint64(100_000)
	const splitAmount = uint64(30_000)

	for i := 0; i < iterations; i++ {
		curPriv := newTestPrivkey(t)
		curPub := mustPubkey(t, curPriv)
		var created MintCashResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
			Recipients: onePubkeyRecipient(curPub, fullAmount),
			Expiry:     happyPathExpirySecs,
		}, &created))

		// ONE proof, built once, submitted twice concurrently.
		newPub := mustPubkey(t, newTestPrivkey(t))
		proof := buildTransferProofEvent(t, curPriv, created.WalletPubkey, "pubkey", newPub, "", splitAmount, nil, time.Now())
		proofJSON := eventJSON(t, proof)
		amt := splitAmount

		clientA := mustConnectBill(t, created.PairingURI, created.CashToken, curPriv)
		clientB := mustConnectBill(t, created.PairingURI, created.CashToken, curPriv)

		var resA, resB CashTransferResult
		var errA, errB error
		barrier := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		params := CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: proofJSON,
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			AmountMillis:  &amt,
		}
		go func() {
			defer wg.Done()
			<-barrier
			errA = clientA.Call(ctxT(t), constants.NIP47MethodCashTransfer, params, &resA)
		}()
		go func() {
			defer wg.Done()
			<-barrier
			errB = clientB.Call(ctxT(t), constants.NIP47MethodCashTransfer, params, &resB)
		}()
		close(barrier)
		wg.Wait()

		wins := 0
		if errA == nil {
			wins++
		}
		if errB == nil {
			wins++
		}
		t.Logf("iter %d: errA=%v errB=%v wins=%d", i, errA, errB, wins)
		require.Equal(t, 1, wins, "identical-proof replay: exactly one of two duplicate proofs must win, got %d", wins)

		// Exactly one split happened and it consumed the source slice: the
		// source wallet was left holding nothing (its value moved into the
		// winner's carved + remainder wallets) and the hub deleted it. A
		// replayed duplicate proof carved off nothing more.
		requireCashWalletDrainedAway(t, admin, hubAppID, created.WalletPubkey,
			// nil: there is no longer a bill method that goes SILENT on a destroyed bill.
			// get_balance is gone from a bill's scopes, and cash_status answers a
			// destroyed bill with a "spent" tombstone on purpose — a holder is entitled
			// to a definitive answer rather than having to infer one from silence. So
			// "the request path stays quiet" stopped being the property; that it answers
			// ONLY a tombstone, carrying no roster, is owned by
			// TestSpec_Archive_IsUnreachableOverNWC and requireSpentBillTombstoned.
			//
			// What remains is the stronger half regardless, read from the Hub's own
			// records: a reply could never have proved the deletion happened.
			nil)
	}
}

// TestAudit_CashTransferExactReplaySequential_Rejected confirms the sequential
// case too: reuse the very same proof a second time (after it already
// succeeded once) and it must be rejected outright, with no further value
// moving — even though a partial split leaves the source slice registered to
// the same identity and the proof still inside its freshness window.
func TestAudit_CashTransferExactReplaySequential_Rejected(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubAppID, admin := createEphemeralCashHub(t, cfg, "audit-exact-replay-seq", nil)
	hubClient := mustConnect(t, hub.Connection)

	curPriv := newTestPrivkey(t)
	curPub := mustPubkey(t, curPriv)
	const fullAmount = uint64(100_000)
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(curPub, fullAmount),
		Expiry:     happyPathExpirySecs,
	}, &created))
	shared := mustConnectBill(t, created.PairingURI, created.CashToken, curPriv)

	newPub := mustPubkey(t, newTestPrivkey(t))
	const splitAmount = uint64(25_000)
	proof := buildTransferProofEvent(t, curPriv, created.WalletPubkey, "pubkey", newPub, "", splitAmount, nil, time.Now())
	proofJSON := eventJSON(t, proof)
	amt := uint64(splitAmount)

	params := CashTransferParams{
		IdentityType:  "pubkey",
		IdentityValue: curPub,
		IdentityEvent: proofJSON,
		NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		AmountMillis:  &amt,
	}
	var res1 CashTransferResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, params, &res1))
	require.EqualValues(t, splitAmount, res1.AmountMillis)

	// Exact same proof again. Under the two-wallet model the first split
	// consumed the source slice and left the wallet holding nothing, so the hub
	// deleted it: only one carve-off ever happened, and it took the whole slice
	// into the two new wallets. The replay therefore reaches no wallet at all
	// and goes unanswered — a stronger outcome than the single-use proof guard
	// alone.
	var res2 CashTransferResult
	requireCashWalletDrainedAway(t, admin, hubAppID, created.WalletPubkey, func(ctx context.Context) error {
		return shared.Call(ctx, constants.NIP47MethodCashTransfer, params, &res2)
	})
}

// TestAudit_CashTransferRateLimit_PerWallet is an OPT-IN probe (it burns a
// wallet's whole hourly cash_transfer/cash_redeem budget) of two properties:
//
//   - the rate limit actually engages on cash_transfer (not only cash_redeem):
//     after the per-wallet hourly allowance is spent, further calls return
//     ERROR_RATE_LIMITED; and
//   - the limit is keyed PER cash_wallet pubkey, so exhausting one wallet's
//     budget leaves a different, freshly-minted wallet fully usable — i.e. the
//     limiter is neither a global lock (which would be a DoS lever) nor
//     bypassable in a way that lets one wallet borrow another's allowance.
//
// Opt-in like the other rate-limit tests: dev's .env sets
// CASH_CLAIM_CALLS_PER_HOUR=0, which the limiter treats as "disabled"
// (nip47/controllers/rate_limiter.go). `just test integration-ratelimits` sets it to
// 20 and runs this with INTEGRATION_RUN_RATE_LIMIT_TESTS=1.
func TestAudit_CashTransferRateLimit_PerWallet(t *testing.T) {
	skipIfEnvUnset(t, "INTEGRATION_RUN_RATE_LIMIT_TESTS")
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-transfer-ratelimit", nil)
	hubClient := mustConnect(t, hub.Connection)

	// The wallet whose budget the loop below spends.
	curPriv := newTestPrivkey(t)
	curPub := mustPubkey(t, curPriv)
	const fullAmount = uint64(1_000_000)
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(curPub, fullAmount),
		Expiry:     happyPathExpirySecs,
	}, &created))
	shared := mustConnectBill(t, created.PairingURI, created.CashToken, curPriv)

	// Real splits cannot exhaust it: a partial split moves the caller's change into
	// a fresh remainder wallet and destroys this one, so every split lands on a new
	// limiter key. Instead, ask for more than the slice holds. That request is
	// well-formed, so the limiter charges it (cash_transfer_controller.go step 4),
	// then step 6 refuses it and the bill survives for the next attempt. With the
	// budget at 20/hour, attempts 1-20 are refused on amount and the 21st on rate.
	newPub := mustPubkey(t, newTestPrivkey(t))
	tooMuch := fullAmount + 1
	proof := buildTransferProofEvent(t, curPriv, created.WalletPubkey, "pubkey", newPub, "", tooMuch, nil, time.Now())
	params := CashTransferParams{
		IdentityType:  "pubkey",
		IdentityValue: curPub,
		IdentityEvent: eventJSON(t, proof),
		NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		AmountMillis:  &tooMuch,
	}
	for i := 1; i <= 20; i++ {
		var res CashTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, params, &res)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	}
	var res CashTransferResult
	err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, params, &res)
	requireNWCErrorCode(t, err, constants.ERROR_RATE_LIMITED)

	// A DIFFERENT wallet is unaffected — the limiter is per-wallet, not global.
	otherPriv := newTestPrivkey(t)
	otherPub := mustPubkey(t, otherPriv)
	var other MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(otherPub, happyPathAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &other))
	otherShared := mustConnectBill(t, other.PairingURI, other.CashToken, otherPriv)
	otherTargetPub := mustPubkey(t, newTestPrivkey(t))
	amt := uint64(happyPathAmountMloki / 2)
	otherProof := buildTransferProofEvent(t, otherPriv, other.WalletPubkey, "pubkey", otherTargetPub, "", amt, nil, time.Now())
	var otherRes CashTransferResult
	require.NoError(t, otherShared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:  "pubkey",
		IdentityValue: otherPub,
		IdentityEvent: eventJSON(t, otherProof),
		NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: otherTargetPub},
		AmountMillis:  &amt,
	}, &otherRes), "a different wallet must not be affected by another wallet's exhausted rate limit")
}
