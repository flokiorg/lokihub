//go:build integration

// spec_cash_atomicity_test.go: R-RACE-2 (NIP-CASH §Atomicity — One Commit). A
// cash-ledger commit is one gorm transaction — it either lands whole or leaves
// nothing behind, bar the request_id mark taken BEFORE it (NIP-CASH §request_id).
// A regtest-only crash hook (testknobs.CommitCrashAt, armed over
// POST /api/test/cash/commit-crash) abandons the open commit at
// cashwallet.StageBeforeCommit as a crash would, so these tests prove the
// all-or-nothing boundary end to end over the real wire: the crashed commit
// undoes every write, yet the request_id it marked first survives the rollback.
// [rec4-local, NEVER-PUSH]
package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/cashwallet"
	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/nip47/cipher"
	"github.com/ohstr/nmilat/nipcash"
)

// cashTransferWithRequestID marshals p and pins request_id to rid. A
// cash_transfer carries a request_id, and the private transport generates one
// only when absent (nwcclient.WithRequestID); pinning it is what lets a crash
// and its replay name the very same id.
func cashTransferWithRequestID(t *testing.T, p CashTransferParams, rid string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(p)
	require.NoError(t, err)
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &m))
	ridJSON, err := json.Marshal(rid)
	require.NoError(t, err)
	m["request_id"] = ridJSON
	out, err := json.Marshal(m)
	require.NoError(t, err)
	return out
}

// TestSpecCash_RRACE2_CommitCrashIsAllOrNothing arms a crash just before COMMIT
// and attempts a partial-split cash_transfer. The call marks its request_id
// (pre-commit), opens the commit, writes every row — and the hook abandons it.
// Nothing survives but that mark: the source slice is wholly intact (no carved
// or remainder wallet, its full value still unclaimed and still spendable), yet
// a replay of the same request_id is REQUEST_REUSED.
func TestSpecCash_RRACE2_CommitCrashIsAllOrNothing(t *testing.T) {
	cfg := requireConfig(t)
	hub, hubAppID, admin := createEphemeralCashHub(t, cfg, "rrace2-commit-crash", nil)
	hooks := requireStackHooks(t, cfg, "POST /cash/commit-crash")
	hubClient := mustConnect(t, hub.Connection)

	currentPriv := newTestPrivkey(t)
	currentPub := mustPubkey(t, currentPriv)

	const fullAmount = happyPathAmountMloki * 4
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: onePubkeyRecipient(currentPub, fullAmount),
		Expiry:     happyPathExpirySecs,
	}, &created))

	newPub := mustPubkey(t, newTestPrivkey(t))
	splitAmount := uint64(happyPathAmountMloki * 2)
	proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", newPub, "", splitAmount, nil, time.Now())
	params := CashTransferParams{
		IdentityType:  "pubkey",
		IdentityValue: currentPub,
		IdentityEvent: eventJSON(t, proof),
		NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		AmountMillis:  &splitAmount,
	}
	crashRID := newRequestID(t)
	rawCall := cashTransferWithRequestID(t, params, crashRID)

	// Arm the crash, then attempt the split: the commit opens, writes, and is
	// abandoned at BeforeCommit as a crash would abandon it.
	hooks.setCommitCrashAt(t, cashwallet.StageBeforeCommit)
	var crashResult CashTransferResult
	crashErr := privateCall(t, created.CashToken, constants.NIP47MethodCashTransfer, rawCall, currentPriv, &crashResult)
	require.Error(t, crashErr, "a commit crashed at BeforeCommit must fail the call")
	require.Empty(t, crashResult.NewWalletToken, "a crashed commit carves no new wallet")
	require.Empty(t, crashResult.RemainderWalletToken, "a crashed commit mints no remainder wallet")

	// Disarm so the redeem below (and anything else on this shared stack) commits.
	hooks.clearCommitCrash(t)

	// (i) The source slice is WHOLLY intact: the rollback undid the source claim,
	// so its full value is still unclaimed on the Hub's own records.
	require.EqualValues(t, fullAmount, unclaimedMlokiForWallet(t, admin, hubAppID, created.WalletPubkey),
		"a crashed commit must leave the source slice's full value untouched")

	// (ii) But the request_id mark, taken before the commit, SURVIVES the
	// rollback: a replay of the same id is REQUEST_REUSED, not a fresh attempt.
	var replayResult CashTransferResult
	replayErr := privateCall(t, created.CashToken, constants.NIP47MethodCashTransfer, rawCall, currentPriv, &replayResult)
	requireNWCErrorCode(t, replayErr, constants.ERROR_REQUEST_REUSED)

	// And the intact slice is genuinely spendable — redeem the whole amount.
	invoice := mintInvoiceFromSimpleWallet(t, cfg, fullAmount, "rrace2 crashed-source full redeem")
	redeemProof := buildClaimProofEvent(t, currentPriv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now())
	source := mustConnectBill(t, created.CashToken, currentPriv)
	var claim ClaimFundsResult
	require.NoError(t, source.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       invoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: currentPub,
		IdentityEvent: eventJSON(t, redeemProof),
	}, &claim))
	require.NotEmpty(t, claim.Preimage, "the wholly-intact source slice must redeem for its full amount")
}

// TestSpecCash_RRACE2_CommittedThenReplayReused is the committed control: with
// the crash OFF, the same partial split commits and the carved wallet exists,
// and a replay of its request_id is REQUEST_REUSED just as the crashed path's
// is — the all-or-nothing boundary consumes the id identically whether the
// commit lands or is abandoned.
//
// Two recipients share the source bill on purpose: splitting one slice off
// leaves the other behind, so the SOURCE bill stays live. That is what lets the
// replay reach the request_id check — a drained bill is answered before it
// (private dispatch resolves the target, I2, before the request_id gate, I6).
func TestSpecCash_RRACE2_CommittedThenReplayReused(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralCashHub(t, cfg, "rrace2-committed-control", nil)
	// Gate on the same hook endpoint so the control runs or skips with the crash test.
	_ = requireStackHooks(t, cfg, "POST /cash/commit-crash")
	hubClient := mustConnect(t, hub.Connection)

	currentPriv := newTestPrivkey(t)
	currentPub := mustPubkey(t, currentPriv)
	coPub := mustPubkey(t, newTestPrivkey(t))

	const fullAmount = happyPathAmountMloki * 4
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: []CashWalletRecipientParam{
			{IdentityType: "pubkey", IdentityValue: currentPub, AmountMillis: fullAmount},
			{IdentityType: "pubkey", IdentityValue: coPub, AmountMillis: happyPathAmountMloki},
		},
		Expiry: happyPathExpirySecs,
	}, &created))

	newPub := mustPubkey(t, newTestPrivkey(t))
	splitAmount := uint64(happyPathAmountMloki * 2)
	proof := buildTransferProofEvent(t, currentPriv, created.WalletPubkey, "pubkey", newPub, "", splitAmount, nil, time.Now())
	params := CashTransferParams{
		IdentityType:  "pubkey",
		IdentityValue: currentPub,
		IdentityEvent: eventJSON(t, proof),
		NewIdentity:   CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		AmountMillis:  &splitAmount,
	}
	commitRID := newRequestID(t)
	rawCall := cashTransferWithRequestID(t, params, commitRID)

	// Crash OFF: the partial split commits and carves the new wallet.
	var result CashTransferResult
	require.NoError(t, privateCall(t, created.CashToken, constants.NIP47MethodCashTransfer, rawCall, currentPriv, &result))
	require.EqualValues(t, splitAmount, result.AmountMillis)
	require.NotEmpty(t, result.NewWalletPubkey)
	require.NotEmpty(t, result.NewWalletToken, "a committed partial split mints the carved wallet")

	// The carved wallet is a real, decodable cash token (nested-encrypted to the
	// caller the same way a normal split delivers it) — it exists.
	carvedCipher, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, result.NewWalletPubkey, currentPriv)
	require.NoError(t, err)
	carvedPlain, err := carvedCipher.Decrypt(result.NewWalletToken)
	require.NoError(t, err)
	_, err = nipcash.Decode(carvedPlain)
	require.NoError(t, err, "the carved wallet's token must decode")

	// The request_id is consumed on the committed path too: a replay of the same
	// id is REQUEST_REUSED (the source bill is still live via its other recipient).
	var replayResult CashTransferResult
	replayErr := privateCall(t, created.CashToken, constants.NIP47MethodCashTransfer, rawCall, currentPriv, &replayResult)
	requireNWCErrorCode(t, replayErr, constants.ERROR_REQUEST_REUSED)
}
