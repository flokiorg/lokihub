//go:build integration

// cash_transfer_audit_connection_key_test.go is the 2026-07-30 dynamic Cash-Hub
// audit's focused live-fire coverage of the connection_key identity mode
// through the NEW cash_transfer split path — the mode the mandate flags as
// getting much less coverage than pubkey/bearer. Every scenario drives the
// real NWC surface over real relay round-trips against the real running
// backend, exercising the Identity-Authority attestation + live-trust re-check
// that gate a connection_key slice's transfer/split, as a malicious or
// compromised holder of a shared cash_wallet connection would.
package integration

import (
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
	"github.com/flokiorg/lokihub/integration/nwcclient"
	"github.com/flokiorg/lokihub/lokicash"
	"github.com/flokiorg/lokihub/nip47/cipher"
)

// connKeyTransferProofTags builds the connection_key-mode-only extra tags a
// transfer proof carries: the connection_key itself plus an e-tag referencing
// the attestation event (mirrors the redeem path's own tag shape in
// cash_redeem_test.go's ConnectionKeyMode subtests).
func connKeyTransferProofTags(connectionKey, attestationID string) nostr.Tags {
	return nostr.Tags{{"connection_key", connectionKey}, {"e", attestationID}}
}

// createConnKeyCashWallet mints a single-recipient connection_key cash_wallet
// under hubClient and returns everything a caller needs to later transfer or
// redeem it: the shared connection, the wallet pubkey, the connection_key, and
// the claimant keypair the IA attests.
func createConnKeyCashWallet(t *testing.T, hubClient *nwcclient.Client, iaPub, connectionKey string, amountMloki uint64) (shared *nwcclient.Client, walletPubkey, claimantPriv, claimantPub string) {
	t.Helper()
	claimantPriv = newTestPrivkey(t)
	claimantPub = mustPubkey(t, claimantPriv)
	var created MintCashResult
	require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodMintCash, MintCashParams{
		Recipients: []CashWalletRecipientParam{
			{IdentityType: "connection_key", IdentityValue: connectionKey, IAPubkey: iaPub, AmountMloki: amountMloki},
		},
		Expiry: happyPathExpirySecs,
	}, &created))
	return mustConnect(t, created.PairingURI), created.WalletPubkey, claimantPriv, claimantPub
}

// TestAudit_CashTransferConnectionKey_PartialSplit_HappyPath proves the
// connection_key identity mode works end to end through the partial-split path:
// a connection_key slice, proven via a live IA attestation, carves a piece off
// into a brand-new dedicated wallet, and BOTH the remainder (still redeemable
// under the same connection_key) and the carve-off (redeemable by the new
// pubkey target) settle for exactly the right amounts. Conservation: remainder
// + carve-off == the original slice, never more.
func TestAudit_CashTransferConnectionKey_PartialSplit_HappyPath(t *testing.T) {
	cfg := requireConfig(t)
	iaPriv := createEphemeralTrustedIA(t, cfg)
	iaPub := mustPubkey(t, iaPriv)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-connkey-partial-split", nil)
	hubClient := mustConnect(t, hub.Connection)

	const fullAmount = uint64(100_000)
	const splitAmount = uint64(30_000)
	connectionKey := newTestConnectionKey(t)
	shared, walletPubkey, claimantPriv, claimantPub := createConnKeyCashWallet(t, hubClient, iaPub, connectionKey, fullAmount)

	// Split 30k off to a fresh pubkey target, authorized by a live IA
	// attestation binding this connection_key to the claimant's pubkey.
	newPriv := newTestPrivkey(t)
	newPub := mustPubkey(t, newPriv)
	attestation := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, time.Hour)
	proof := buildTransferProofEvent(t, claimantPriv, walletPubkey, "pubkey", newPub, "", splitAmount,
		connKeyTransferProofTags(connectionKey, attestation.ID), time.Now())

	amt := splitAmount
	var res CashTransferResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:     "connection_key",
		IdentityValue:    connectionKey,
		IdentityEvent:    eventJSON(t, proof),
		AttestationEvent: eventJSON(t, attestation),
		NewIdentity:      CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		AmountMloki:      &amt,
	}, &res))
	require.EqualValues(t, splitAmount, res.AmountMloki)
	require.NotNil(t, res.RemainingAmountMloki)
	require.EqualValues(t, fullAmount-splitAmount, *res.RemainingAmountMloki)
	require.NotEmpty(t, res.NewWalletToken, "a connection_key partial split must spin off a dedicated wallet")

	// Source real balance must equal exactly the remainder — no double-spend.
	var bal GetBalanceResult
	require.NoError(t, shared.Call(ctxT(t), "get_balance", struct{}{}, &bal))
	require.EqualValues(t, fullAmount-splitAmount, bal.Balance)

	// The remainder is still redeemable under the SAME connection_key (fresh
	// attestation + proof), for exactly the remainder amount.
	remInvoice := mintInvoiceFromSimpleWallet(t, cfg, fullAmount-splitAmount, "audit connkey remainder redeem")
	remAttestation := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, time.Hour)
	remProof := buildClaimProofEvent(t, claimantPriv, walletPubkey, remInvoice.PaymentHash,
		connKeyTransferProofTags(connectionKey, remAttestation.ID), time.Now())
	var remClaim ClaimFundsResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:          remInvoice.Invoice,
		IdentityType:     "connection_key",
		IdentityValue:    connectionKey,
		IdentityEvent:    eventJSON(t, remProof),
		AttestationEvent: eventJSON(t, remAttestation),
	}, &remClaim))
	require.NotEmpty(t, remClaim.Preimage)

	// The carved-off wallet is genuinely funded and redeemable by the pubkey
	// target (decrypt the inner token with the caller's own privkey).
	c, err := cipher.NewNip47Cipher(constants.ENCRYPTION_TYPE_NIP44_V2, res.NewWalletPubkey, claimantPriv)
	require.NoError(t, err)
	dec, err := c.Decrypt(res.NewWalletToken)
	require.NoError(t, err)
	tok, err := lokicash.Decode(dec)
	require.NoError(t, err)
	newClient := mustConnect(t, nwcURIFromLokicash(tok))
	newInvoice := mintInvoiceFromSimpleWallet(t, cfg, splitAmount, "audit connkey carveoff redeem")
	newProof := buildClaimProofEvent(t, newPriv, res.NewWalletPubkey, newInvoice.PaymentHash, nil, time.Now())
	var newClaim ClaimFundsResult
	require.NoError(t, newClient.Call(ctxT(t), constants.NIP47MethodCashRedeem, ClaimFundsParams{
		Invoice:       newInvoice.Invoice,
		IdentityType:  "pubkey",
		IdentityValue: newPub,
		IdentityEvent: eventJSON(t, newProof),
	}, &newClaim))
	require.NotEmpty(t, newClaim.Preimage)
}

// TestAudit_CashTransferConnectionKey_RevokedIA_Rejected confirms the live
// Identity-Authority trust re-check the split path performs (same one
// cash_redeem does): if the IA that vouches for a connection_key slice is
// revoked AFTER the slice is minted but BEFORE its owner transfers it, the
// transfer/split MUST be refused (ERROR_RESTRICTED) and the funds must stay put
// — a compromised/retired IA must be cut off immediately for cash_transfer,
// not only cash_redeem.
//
// Attacker model: an IA key is compromised and revoked by the operator; the
// attacker holding a connection_key slice attested by that now-revoked IA must
// not be able to launder it out into a fresh wallet before the revocation
// takes hold.
func TestAudit_CashTransferConnectionKey_RevokedIA_Rejected(t *testing.T) {
	cfg := requireConfig(t)
	admin, ok := newAdminClient(cfg)
	if !ok {
		t.Skip("skipping: admin_api not configured")
	}
	iaPriv := createEphemeralTrustedIA(t, cfg)
	iaPub := mustPubkey(t, iaPriv)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-connkey-revoked-ia", nil)
	hubClient := mustConnect(t, hub.Connection)

	const fullAmount = uint64(80_000)
	connectionKey := newTestConnectionKey(t)
	shared, walletPubkey, claimantPriv, claimantPub := createConnKeyCashWallet(t, hubClient, iaPub, connectionKey, fullAmount)

	// Revoke the IA now (createEphemeralTrustedIA's own t.Cleanup will attempt a
	// second, harmless revoke later — logged, not failed).
	require.NoError(t, admin.deleteIdentityAuthority(iaPub))

	newPub := mustPubkey(t, newTestPrivkey(t))
	attestation := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, time.Hour)

	// A full transfer (in-place reassignment) must be refused...
	fullProof := buildTransferProofEvent(t, claimantPriv, walletPubkey, "pubkey", newPub, "", fullAmount,
		connKeyTransferProofTags(connectionKey, attestation.ID), time.Now())
	var fullRes CashTransferResult
	err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:     "connection_key",
		IdentityValue:    connectionKey,
		IdentityEvent:    eventJSON(t, fullProof),
		AttestationEvent: eventJSON(t, attestation),
		NewIdentity:      CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
	}, &fullRes)
	requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)

	// ...and so must a partial split.
	half := fullAmount / 2
	splitProof := buildTransferProofEvent(t, claimantPriv, walletPubkey, "pubkey", newPub, "", half,
		connKeyTransferProofTags(connectionKey, attestation.ID), time.Now())
	var splitRes CashTransferResult
	err = shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:     "connection_key",
		IdentityValue:    connectionKey,
		IdentityEvent:    eventJSON(t, splitProof),
		AttestationEvent: eventJSON(t, attestation),
		NewIdentity:      CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: newPub},
		AmountMloki:      &half,
	}, &splitRes)
	requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)

	// Funds untouched: the slice's whole value is still on the source wallet.
	var bal GetBalanceResult
	require.NoError(t, shared.Call(ctxT(t), "get_balance", struct{}{}, &bal))
	require.EqualValues(t, fullAmount, bal.Balance, "a revoked-IA transfer must not move any funds")

	// Re-register the IA so the ephemeral cleanup can reclaim the wallet's funds
	// on delete (a connection_key wallet whose IA is untrusted is still
	// admin-reclaimable, but re-trusting keeps teardown on the ordinary path).
	require.NoError(t, admin.registerIdentityAuthority(iaPub, ephemeralFixtureNamePrefix+" re-trusted for cleanup"))
}

// TestAudit_CashTransferConnectionKey_NewTargetUntrustedIA_Rejected confirms
// that when the CALLER is properly authorized but names a NEW connection_key
// target whose IA is not trusted, the split is refused — the same identity-shape
// + live-trust validation mint_cash applies to a recipient must gate
// the split's new wallet too, so an authorized owner can't mint a spun-off
// wallet vouched for by a bogus/untrusted authority.
func TestAudit_CashTransferConnectionKey_NewTargetUntrustedIA_Rejected(t *testing.T) {
	cfg := requireConfig(t)
	iaPriv := createEphemeralTrustedIA(t, cfg)
	iaPub := mustPubkey(t, iaPriv)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-connkey-bad-target-ia", nil)
	hubClient := mustConnect(t, hub.Connection)

	const fullAmount = uint64(60_000)
	connectionKey := newTestConnectionKey(t)
	shared, walletPubkey, claimantPriv, claimantPub := createConnKeyCashWallet(t, hubClient, iaPub, connectionKey, fullAmount)

	// A brand-new connection_key target vouched for by a never-registered
	// (untrusted) IA pubkey.
	untrustedIAPub := mustPubkey(t, newTestPrivkey(t))
	targetConnKey := newTestConnectionKey(t)
	half := fullAmount / 2
	attestation := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, time.Hour)
	proof := buildTransferProofEvent(t, claimantPriv, walletPubkey, "connection_key", targetConnKey, untrustedIAPub, half,
		connKeyTransferProofTags(connectionKey, attestation.ID), time.Now())

	var res CashTransferResult
	err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:     "connection_key",
		IdentityValue:    connectionKey,
		IdentityEvent:    eventJSON(t, proof),
		AttestationEvent: eventJSON(t, attestation),
		NewIdentity:      CashTransferNewIdentityParam{IdentityType: "connection_key", IdentityValue: targetConnKey, IAPubkey: untrustedIAPub},
		AmountMloki:      &half,
	}, &res)
	requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)

	// Nothing moved.
	var bal GetBalanceResult
	require.NoError(t, shared.Call(ctxT(t), "get_balance", struct{}{}, &bal))
	require.EqualValues(t, fullAmount, bal.Balance, "a rejected untrusted-target split must not move any funds")
}

// TestAudit_CashTransferConnectionKey_AttestationForWrongClaimant_Rejected is
// the transfer-path analogue of cash_redeem_test.go's attestation-theft test: a
// kind-35522 IA attestation is a signed, relayable event, not a secret. An
// attacker who intercepts a real one (issued by the trusted IA, for the real
// connection_key, naming the REAL claimant) but signs the transfer proof with
// their OWN key must not be able to redirect/steal the slice by splitting it
// out into a wallet they control.
func TestAudit_CashTransferConnectionKey_AttestationForWrongClaimant_Rejected(t *testing.T) {
	cfg := requireConfig(t)
	iaPriv := createEphemeralTrustedIA(t, cfg)
	iaPub := mustPubkey(t, iaPriv)
	hub, _, _ := createEphemeralCashHub(t, cfg, "audit-connkey-attestation-theft", nil)
	hubClient := mustConnect(t, hub.Connection)

	const fullAmount = uint64(60_000)
	connectionKey := newTestConnectionKey(t)
	shared, walletPubkey, realClaimantPriv, realClaimantPub := createConnKeyCashWallet(t, hubClient, iaPub, connectionKey, fullAmount)

	// A real attestation naming the REAL claimant.
	attestation := buildIAAttestationEvent(t, iaPriv, connectionKey, realClaimantPub, time.Hour)

	// Attacker signs their own proof, reusing the intercepted attestation.
	attackerPriv := newTestPrivkey(t)
	attackerTargetPub := mustPubkey(t, newTestPrivkey(t))
	half := fullAmount / 2
	attackerProof := buildTransferProofEvent(t, attackerPriv, walletPubkey, "pubkey", attackerTargetPub, "", half,
		connKeyTransferProofTags(connectionKey, attestation.ID), time.Now())

	var res CashTransferResult
	err := shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:     "connection_key",
		IdentityValue:    connectionKey,
		IdentityEvent:    eventJSON(t, attackerProof),
		AttestationEvent: eventJSON(t, attestation),
		NewIdentity:      CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: attackerTargetPub},
		AmountMloki:      &half,
	}, &res)
	requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)

	// The real owner can still split their own slice using the same attestation.
	realTargetPriv := newTestPrivkey(t)
	realTargetPub := mustPubkey(t, realTargetPriv)
	realAttestation := buildIAAttestationEvent(t, iaPriv, connectionKey, realClaimantPub, time.Hour)
	realProof := buildTransferProofEvent(t, realClaimantPriv, walletPubkey, "pubkey", realTargetPub, "", half,
		connKeyTransferProofTags(connectionKey, realAttestation.ID), time.Now())
	var realRes CashTransferResult
	require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodCashTransfer, CashTransferParams{
		IdentityType:     "connection_key",
		IdentityValue:    connectionKey,
		IdentityEvent:    eventJSON(t, realProof),
		AttestationEvent: eventJSON(t, realAttestation),
		NewIdentity:      CashTransferNewIdentityParam{IdentityType: "pubkey", IdentityValue: realTargetPub},
		AmountMloki:      &half,
	}, &realRes))
	require.EqualValues(t, half, realRes.AmountMloki)
}
