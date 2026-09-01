package controllers

import (
	"encoding/json"
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

// legacyPartialSplitResult mirrors the cash_transfer response shape a client
// written BEFORE the two-wallet partial-split rewrite would still understand:
// the pre-rewrite contract split off the carved amount into a fresh wallet
// (new_wallet_pubkey/new_wallet_token) but left the REMAINDER decremented in
// place, on the SAME source connection, under the caller's own unchanged
// identity — reported only via remaining_amount_mloki as a plain number. Such
// a client has no reason to expect (and, by construction here, does not
// declare) a remainder_wallet_pubkey/remainder_wallet_token pair, since one
// never existed in the contract it was written against.
type legacyPartialSplitResult struct {
	AmountMloki          uint64  `json:"amount_mloki"`
	IdentityType         string  `json:"identity_type"`
	IdentityValue        string  `json:"identity_value,omitempty"`
	RemainingAmountMloki *uint64 `json:"remaining_amount_mloki,omitempty"`
	NewWalletPubkey      string  `json:"new_wallet_pubkey,omitempty"`
	NewWalletToken       string  `json:"new_wallet_token,omitempty"`
	// Deliberately NO remainder_wallet_pubkey / remainder_wallet_token fields.
}

// TestHandleCashTransferEvent_PartialSplit_LegacyClientSilentlyStrandsRemainder
// is a UX/experience-review finding (Cash Hub consolidate/split audit,
// 2026-08-29; partially mitigated during a later full-review pass): a client
// built against the OLD partial-split contract — where the remainder stayed
// on the source connection, under the same identity, decremented in place —
// loses track of its own change under the NEW two-wallet contract, since the
// legacy response shape has no field to carry remainder_wallet_token.
//
// Sequence this proves:
//  1. A partial cash_transfer succeeds and returns a response that, decoded
//     into the OLD/legacy result shape (above), decodes cleanly (no error) and
//     reports remaining_amount_mloki > 0 — exactly what a legacy client would
//     read as "I still have this much on the connection I already hold."
//  2. The legacy struct has no field to carry remainder_wallet_token, so a
//     client using it never learns a brand-new wallet now holds that value —
//     encoding/json silently drops unknown object members on decode, it does
//     not error.
//  3. Acting on the belief step 1 leaves a legacy client with, it naturally
//     tries to use the SAME connection/identity again (the only one it has
//     any handle on) for the remainder. This now fails ERROR_NOT_FOUND, with
//     a message that DOES include a breadcrumb toward where the value went
//     (fixed as part of the full-review pass) — a legacy client can't recover
//     automatically from this alone (it wasn't written to parse a breadcrumb
//     out of an error string), but an operator or a human reading the error
//     now has an actionable lead instead of a dead end.
//
// The funds are not destroyed (they sit, address-recoverable, in the ledger,
// findable via spun_off_to_wallet_app_id). What remains open, deliberately
// not fixed by the breadcrumb above: NIP-47's own capability advertisement
// (publish_nip47_info.go) never versions this wire-shape change —
// cash_transfer's method name is unchanged, so there is still no way for a
// client to detect the new contract BEFORE a live call fails. A capability-
// version bump was considered and deliberately deferred: it wouldn't help any
// client that paired before the rewrite and doesn't re-fetch capabilities per
// call, and there's no evidence of a real third-party client on the old
// contract today — revisit if/when one exists.
func TestHandleCashTransferEvent_PartialSplit_LegacyClientSilentlyStrandsRemainder(t *testing.T) {
	svc, err := tests.CreateTestService(t)
	require.NoError(t, err)
	defer svc.Remove()

	hub := tests.CreateCashHub(t, svc, 100_000, 3600)
	wallet, _, err := svc.AppsService.CreateApp(
		"cash-wallet", "", 0, constants.BUDGET_RENEWAL_NEVER, nil,
		[]string{constants.CASH_REDEEM_SCOPE, constants.CASH_TRANSFER_SCOPE, constants.GET_BALANCE_SCOPE},
		db.AppKindCashWallet, &hub.ID, db.ParentKindCash, nil,
	)
	require.NoError(t, err)
	tests.FundApp(svc, wallet.ID, 200_000, tests.RandomHex32())
	mockLN := svc.LNClient.(*tests.MockLn)
	mockLN.Pubkey = "03cbd788f5b22bd56e2714bff756372d2293504c064e03250ed16a4dd80ad70e2c"
	mockLN.MakeInvoiceQueue = []*lnclient.Transaction{
		{Type: "incoming", Invoice: tests.MockInvoice, PaymentHash: tests.MockPaymentHash, Preimage: "preimage-carved", Amount: 2000},
		{Type: "incoming", Invoice: tests.MockLNClientHoldTransaction.Invoice, PaymentHash: tests.MockLNClientHoldTransaction.PaymentHash, Preimage: "preimage-remainder", Amount: 3000},
	}

	currentPrivkey := nostr.GeneratePrivateKey()
	currentPubkey, _ := nostr.GetPublicKey(currentPrivkey)
	require.NoError(t, svc.AppsService.CreateCashWalletClaims(wallet.ID, []db.CashWalletClaim{
		{IdentityType: db.CashIdentityPubkey, IdentityValue: currentPubkey, AmountMloki: 5000},
	}))

	newPubkey, _ := nostr.GetPublicKey(nostr.GeneratePrivateKey())
	amount := uint64(2000)
	proof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 2000, nil, time.Now())
	response := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, proof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
		AmountMloki:   &amount,
	})
	require.Nil(t, response.Error)

	// Marshal exactly as the real NIP-47 dispatch path would before encrypting
	// onto the wire, then decode with the OLD/legacy shape only.
	wireBytes, err := json.Marshal(response.Result)
	require.NoError(t, err)

	var legacy legacyPartialSplitResult
	require.NoError(t, json.Unmarshal(wireBytes, &legacy),
		"a legacy client's decode must not error -- unknown JSON members (remainder_wallet_token) are silently dropped, not rejected")

	require.NotNil(t, legacy.RemainingAmountMloki, "the legacy client reads a nonzero remaining balance and has no reason to suspect anything is missing")
	assert.EqualValues(t, 3000, *legacy.RemainingAmountMloki)

	// Prove the raw wire JSON DOES carry the remainder token -- so what
	// follows is a client-side blind spot, not a server-side omission.
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(wireBytes, &raw))
	require.Contains(t, raw, "remainder_wallet_token", "the server did deliver the remainder -- the legacy struct just has no field for it")
	assert.NotEmpty(t, raw["remainder_wallet_token"])

	// The legacy client's only lead is the connection it already holds
	// (wallet/currentPubkey) -- exactly what the OLD contract would have kept
	// serving the remainder on. Acting on that belief now fails, but the
	// failure message must carry a breadcrumb toward where the value went
	// (noSliceRegisteredMessage, cash_transfer_controller.go).
	retryProof := buildTransferProofEvent(t, currentPrivkey, *wallet.WalletPubkey, db.CashIdentityPubkey, newPubkey, "", 3000, nil, time.Now())
	retry := handleCashTransferFor(t, svc, NewTestNip47Controller(svc), wallet, cashTransferParams{
		IdentityType:  db.CashIdentityPubkey,
		IdentityValue: currentPubkey,
		IdentityEvent: mustMarshal(t, retryProof),
		NewIdentity:   cashTransferNewIdentityParam{IdentityType: db.CashIdentityPubkey, IdentityValue: newPubkey},
	})
	require.NotNil(t, retry.Error, "the legacy client's natural next call, against the connection it still believes holds its change, must fail")
	assert.Equal(t, constants.ERROR_NOT_FOUND, retry.Error.Code)
	assert.Contains(t, retry.Error.Message, "split off into a new dedicated wallet",
		"the failure message must give a forwarding hint toward the new wallet the value actually moved to")
	assert.Contains(t, retry.Error.Message, "list_recipients")
	t.Logf("legacy client's follow-up call failed with: %q (breadcrumb present)", retry.Error.Message)
}
