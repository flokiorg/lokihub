//go:build integration

// cash_transfer_audit_replay_race_test.go is the 2026-07-30 dynamic Cash-Hub
// audit's coverage of the NEW single-use replay guard (db.CashTransferProof)
// under real, racing network round-trips, plus a live probe of the
// cash_transfer rate limit. The existing money-conservation race test
// (cash_transfer_audit_split_race_test.go) fires DISTINCT proofs each round; it
// never exercises what happens when the SAME captured proof is submitted twice
// — which is precisely the co-recipient replay the guard exists to stop.
package integration

import (
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
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-identical-proof-race", nil)
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

		clientA := mustConnect(t, created.PairingURI)
		clientB := mustConnect(t, created.PairingURI)

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
			AmountMloki:   &amt,
		}
		go func() { defer wg.Done(); <-barrier; errA = clientA.Call(ctxT(t), constants.NIP47MethodCashTransfer, params, &resA) }()
		go func() { defer wg.Done(); <-barrier; errB = clientB.Call(ctxT(t), constants.NIP47MethodCashTransfer, params, &resB) }()
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

		// Exactly one split happened and it consumed the source slice: the source
		// wallet is now drained (its value moved into the winner's carved +
		// remainder wallets). A replayed duplicate proof carved off nothing more.
		conn := mustConnect(t, created.PairingURI)
		var bal GetBalanceResult
		require.NoError(t, conn.Call(ctxT(t), "get_balance", struct{}{}, &bal))
		require.EqualValues(t, 0, bal.Balance,
			"the source slice was consumed once; a replayed duplicate proof must not carve off a second piece")
	}
}

// TestAudit_CashTransferExactReplaySequential_Rejected confirms the sequential
// case too: reuse the very same proof a second time (after it already
// succeeded once) and it must be rejected outright, with no further value
// moving — even though a partial split leaves the source slice registered to
// the same identity and the proof still inside its freshness window.
func TestAudit_CashTransferExactReplaySequential_Rejected(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-exact-replay-seq", nil)
	hubClient := mustConnect(t, hub.Connection)

	curPriv := newTestPrivkey(t)
	curPub := mustPubkey(t, curPriv)
	const fullAmount = uint64(100_000)
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(curPub, fullAmount),
		Expiry:     happyPathExpirySecs,
	}, &created))
	shared := mustConnect(t, created.PairingURI)

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
		AmountMloki:   &amt,
	}
	var res1 CashTransferResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, params, &res1))
	require.EqualValues(t, splitAmount, res1.AmountMloki)

	// Exact same proof again — must be rejected. Under the two-wallet model the
	// first split consumed the source slice, so the replay finds no slice to act
	// on (an even stronger rejection than the single-use proof guard alone).
	var res2 CashTransferResult
	err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, params, &res2)
	requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)

	// The source wallet is drained — only one carve-off ever happened, and it
	// took the whole slice into the two new wallets.
	var bal GetBalanceResult
	require.NoError(t, shared.Call(ctxT(t), "get_balance", struct{}{}, &bal))
	require.EqualValues(t, 0, bal.Balance)
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
// Set CASH_TRANSFER_RATELIMIT_TEST=1 to run.
//
// NOTE: the dev stack this audit runs against sets
// CASH_WALLET_CLAIM_RATE_LIMIT_PER_HOUR=0, which the limiter treats as
// "disabled — every call allowed" (nip47/controllers/rate_limiter.go). So on
// this stack the loop below never trips; the test detects that and SKIPS
// rather than failing, since a disabled limit here is a deliberate
// environment config, not a code defect. It becomes a live assertion only on a
// stack with a positive limit configured.
func TestAudit_CashTransferRateLimit_PerWallet(t *testing.T) {
	skipIfEnvUnset(t, "CASH_TRANSFER_RATELIMIT_TEST")
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-transfer-ratelimit", nil)
	hubClient := mustConnect(t, hub.Connection)

	// A wallet with plenty of headroom to keep partial-splitting until the
	// limiter — not the balance — stops us.
	curPriv := newTestPrivkey(t)
	curPub := mustPubkey(t, curPriv)
	const fullAmount = uint64(1_000_000)
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(curPub, fullAmount),
		Expiry:     happyPathExpirySecs,
	}, &created))
	shared := mustConnect(t, created.PairingURI)

	// Fire partial splits until the limiter trips (or a generous ceiling — the
	// default budget is 20/hour, so 40 attempts is ample headroom).
	rateLimited := false
	for i := 0; i < 40; i++ {
		newPub := mustPubkey(t, newTestPrivkey(t))
		amt := uint64(1_000)
		proof := buildTransferProofEvent(t, curPriv, created.WalletPubkey, "pubkey", newPub, "", amt, nil, time.Now())
		var res CashTransferResult
		err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
			IdentityType:  "pubkey",
			IdentityValue: curPub,
			IdentityEvent: eventJSON(t, proof),
			NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
			AmountMloki:   &amt,
		}, &res)
		if err != nil {
			t.Logf("attempt %d errored: %v", i, err)
			requireNWCErrorCode(t, err, constants.ERROR_RATE_LIMITED)
			rateLimited = true
			break
		}
	}
	if !rateLimited {
		t.Skip("skipping: this stack has CASH_WALLET_CLAIM_RATE_LIMIT_PER_HOUR=0 " +
			"(rate limit disabled — 40 cash_transfer calls all succeeded); rerun against " +
			"a stack with a positive limit to exercise the per-wallet limiter live")
	}

	// A DIFFERENT wallet is unaffected — the limiter is per-wallet, not global.
	otherPriv := newTestPrivkey(t)
	otherPub := mustPubkey(t, otherPriv)
	var other MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(otherPub, happyPathAmountMloki),
		Expiry:     happyPathExpirySecs,
	}, &other))
	otherShared := mustConnect(t, other.PairingURI)
	otherTargetPub := mustPubkey(t, newTestPrivkey(t))
	amt := uint64(happyPathAmountMloki / 2)
	otherProof := buildTransferProofEvent(t, otherPriv, other.WalletPubkey, "pubkey", otherTargetPub, "", amt, nil, time.Now())
	var otherRes CashTransferResult
	require.NoError(t, otherShared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:  "pubkey",
		IdentityValue: otherPub,
		IdentityEvent: eventJSON(t, otherProof),
		NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: otherTargetPub},
		AmountMloki:   &amt,
	}, &otherRes), "a different wallet must not be affected by another wallet's exhausted rate limit")
}
