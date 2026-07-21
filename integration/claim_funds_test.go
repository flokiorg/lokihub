//go:build integration

// claim_funds_test.go covers the shared-wallet, proof-gated claim model this
// suite's create_jit_wallet scenarios (jit_hub_test.go) create wallets for:
// multiple independent recipients sharing one connection, each proving their
// own identity to claim their own slice, and the security properties that
// make sharing that connection safe (see nip47/controllers/claim_funds_controller.go's
// own doc comments for the design rationale this exercises end to end).
//
// Multi-recipient wallet creation is itself already covered by
// jit_hub_test.go's CreateWallet_MultipleRecipients_OneSharedWallet — this
// file focuses on the claim side.
package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/stretchr/testify/require"

	"github.com/flokiorg/lokihub/constants"
)

func TestClaimFunds(t *testing.T) {
	cfg := requireConfig(t)
	hub, _, _ := createEphemeralJITHub(t, cfg, "claim-funds-jit-hub", nil)
	testClaimFunds(t, cfg, hub)
}

func testClaimFunds(t *testing.T, cfg *Config, hub JITHubConfig) {
	hubClient := mustConnect(t, hub.Connection)

	t.Run("MultiRecipient_EachClaimsOwnSliceIndependently", func(t *testing.T) {
		type recipient struct {
			privkey string
			pubkey  string
			amount  uint64
		}
		recipients := make([]recipient, 3)
		params := make([]JITWalletRecipientParam, 3)
		for i := range recipients {
			priv := newTestPrivkey(t)
			pub, err := nostr.GetPublicKey(priv)
			require.NoError(t, err)
			amount := happyPathAmountMloki * uint64(i+1) // distinct amounts per recipient
			recipients[i] = recipient{privkey: priv, pubkey: pub, amount: amount}
			params[i] = JITWalletRecipientParam{IdentityType: "pubkey", IdentityValue: pub, AmountMloki: amount}
		}

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: params,
			Expiry:     happyPathExpirySecs,
		}, &created))
		require.NotEmpty(t, created.PairingURI)

		// All three recipients share the SAME connection.
		shared := mustConnect(t, created.PairingURI)

		var totalMloki uint64
		for _, r := range recipients {
			totalMloki += r.amount
		}
		var balanceBefore GetBalanceResult
		require.NoError(t, shared.Call(ctxT(t), "get_balance", struct{}{}, &balanceBefore))
		require.EqualValues(t, totalMloki, balanceBefore.Balance)

		// Recipient 0 claims their slice. Recipients 1 and 2 must remain
		// fully intact and independently claimable afterward — this is the
		// property that makes the shared-pool model safe.
		invoice0 := mintInvoiceFromSimpleWallet(t, cfg, recipients[0].amount, "integration multi-recipient claim 0")
		proof0 := buildClaimProofEvent(t, recipients[0].privkey, created.WalletPubkey, invoice0.PaymentHash, nil, time.Now())
		var result0 ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       invoice0.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: recipients[0].pubkey,
			IdentityEvent: eventJSON(t, proof0),
		}, &result0))
		require.NotEmpty(t, result0.Preimage)

		var recipientsAfter0 ListRecipientsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodListRecipients, struct{}{}, &recipientsAfter0))
		require.Len(t, recipientsAfter0.Recipients, 3)
		for _, r := range recipientsAfter0.Recipients {
			if r.IdentityValue == recipients[0].pubkey {
				require.True(t, r.Claimed)
			} else {
				require.False(t, r.Claimed, "other recipients must remain unclaimed after recipient 0's claim")
			}
		}

		// Recipient 1 claims independently — must succeed without any
		// interference from recipient 0's already-settled claim.
		invoice1 := mintInvoiceFromSimpleWallet(t, cfg, recipients[1].amount, "integration multi-recipient claim 1")
		proof1 := buildClaimProofEvent(t, recipients[1].privkey, created.WalletPubkey, invoice1.PaymentHash, nil, time.Now())
		var result1 ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       invoice1.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: recipients[1].pubkey,
			IdentityEvent: eventJSON(t, proof1),
		}, &result1))
		require.NotEmpty(t, result1.Preimage)

		// Recipient 2's slice must still be fully intact.
		var recipientsAfter1 ListRecipientsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodListRecipients, struct{}{}, &recipientsAfter1))
		for _, r := range recipientsAfter1.Recipients {
			if r.IdentityValue == recipients[2].pubkey {
				require.False(t, r.Claimed)
				require.EqualValues(t, recipients[2].amount, r.AmountMloki)
			}
		}
	})

	t.Run("WrongIdentity_NoMatchingSlice_Rejected", func(t *testing.T) {
		realPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(realPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		// An outsider who genuinely owns their own key (valid signature) but
		// has no slice on this wallet.
		outsiderPriv := newTestPrivkey(t)
		outsiderPub, err := nostr.GetPublicKey(outsiderPriv)
		require.NoError(t, err)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration wrong-identity test")
		proof := buildClaimProofEvent(t, outsiderPriv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now())
		var result ClaimFundsResult
		err = shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: outsiderPub,
			IdentityEvent: eventJSON(t, proof),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_NOT_FOUND)
	})

	t.Run("ProofBoundToWrongInvoice_Rejected", func(t *testing.T) {
		// The core audit-finding scenario: since the connection may be
		// shared/public, anyone holding it can decrypt every claim_funds
		// request sent on it — an attacker who intercepts a valid proof
		// must not be able to redirect the payout to a different invoice.
		beneficiaryPriv := newTestPrivkey(t)
		beneficiaryPub, err := nostr.GetPublicKey(beneficiaryPriv)
		require.NoError(t, err)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		boundInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration proof-binding test (bound)")
		attackerInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration proof-binding test (attacker)")

		// Proof is bound to boundInvoice's payment hash...
		proof := buildClaimProofEvent(t, beneficiaryPriv, created.WalletPubkey, boundInvoice.PaymentHash, nil, time.Now())

		// ...but submitted against a DIFFERENT invoice.
		var result ClaimFundsResult
		err = shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       attackerInvoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: beneficiaryPub,
			IdentityEvent: eventJSON(t, proof),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)

		// The legitimate, correctly-bound claim must still succeed afterward.
		correctResult := ClaimFundsResult{}
		proof2 := buildClaimProofEvent(t, beneficiaryPriv, created.WalletPubkey, boundInvoice.PaymentHash, nil, time.Now())
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       boundInvoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: beneficiaryPub,
			IdentityEvent: eventJSON(t, proof2),
		}, &correctResult))
		require.NotEmpty(t, correctResult.Preimage)
	})

	t.Run("ReplayAcrossWallets_Rejected", func(t *testing.T) {
		beneficiaryPriv := newTestPrivkey(t)
		beneficiaryPub, err := nostr.GetPublicKey(beneficiaryPriv)
		require.NoError(t, err)

		// The same identity happens to have a slice on two different wallets.
		var walletA, walletB CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &walletA))
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &walletB))

		sharedB := mustConnect(t, walletB.PairingURI)
		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration cross-wallet replay test")

		// Proof is bound (d-tag) to wallet A's pubkey...
		proof := buildClaimProofEvent(t, beneficiaryPriv, walletA.WalletPubkey, invoice.PaymentHash, nil, time.Now())

		// ...but submitted against wallet B.
		var result ClaimFundsResult
		err = sharedB.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: beneficiaryPub,
			IdentityEvent: eventJSON(t, proof),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("GetBudget_Rejected_GetInfo_Allowed", func(t *testing.T) {
		beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		var budget GetBudgetResult
		err = shared.Call(ctxT(t), "get_budget", struct{}{}, &budget)
		requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)

		var info GetInfoResult
		require.NoError(t, shared.Call(ctxT(t), "get_info", struct{}{}, &info))
	})

	t.Run("ListTransactionsAndLookupInvoice_NotGranted", func(t *testing.T) {
		beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		var txs ListTransactionsResult
		err = shared.Call(ctxT(t), "list_transactions", ListTransactionsParams{Limit: 10}, &txs)
		requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)

		var lookup MakeInvoiceResult
		err = shared.Call(ctxT(t), "lookup_invoice", struct{}{}, &lookup)
		requireNWCErrorCode(t, err, constants.ERROR_RESTRICTED)
	})

	t.Run("ListRecipients_ShowsClaimedAndUnclaimedStatus", func(t *testing.T) {
		claimedPriv := newTestPrivkey(t)
		claimedPub, err := nostr.GetPublicKey(claimedPriv)
		require.NoError(t, err)
		unclaimedPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "pubkey", IdentityValue: claimedPub, AmountMloki: happyPathAmountMloki},
				{IdentityType: "pubkey", IdentityValue: unclaimedPub, AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration list_recipients status test")
		proof := buildClaimProofEvent(t, claimedPriv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now())
		var claimResult ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: claimedPub,
			IdentityEvent: eventJSON(t, proof),
		}, &claimResult))

		var recipients ListRecipientsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodListRecipients, struct{}{}, &recipients))
		require.Len(t, recipients.Recipients, 2)
		for _, r := range recipients.Recipients {
			if r.IdentityValue == claimedPub {
				require.True(t, r.Claimed)
				require.NotNil(t, r.ClaimedAt)
			} else {
				require.False(t, r.Claimed)
				require.Nil(t, r.ClaimedAt)
			}
		}
	})

	t.Run("ConnectionKeyMode_ClaimHappyPath", func(t *testing.T) {
		iaPriv := createEphemeralTrustedIA(t, cfg)
		connectionKey := newTestConnectionKey(t)
		claimantPriv := newTestPrivkey(t)
		claimantPub := mustPubkey(t, claimantPriv)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "connection_key", IdentityValue: connectionKey, IAPubkey: mustPubkey(t, iaPriv), AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration connection_key claim test")
		attestation := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, time.Hour)
		proof := buildClaimProofEvent(t, claimantPriv, created.WalletPubkey, invoice.PaymentHash,
			nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

		var result ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:          invoice.Invoice,
			IdentityType:     "connection_key",
			IdentityValue:    connectionKey,
			IdentityEvent:    eventJSON(t, proof),
			AttestationEvent: eventJSON(t, attestation),
		}, &result))
		require.NotEmpty(t, result.Preimage, "the full connection_key claim path (proof + live IA attestation verification) must pay out")
	})

	t.Run("ConnectionKeyMode_AttestationForDifferentClaimant_Rejected", func(t *testing.T) {
		// A kind-35522 IA attestation is itself a signed, relayable nostr
		// event - not a secret. An attacker who intercepts/discovers a real
		// one (issued by the real, trusted IA, for the real connection_key on
		// this wallet, but naming a real claimant) has everything except that
		// claimant's private key. Signing their own claim proof and reusing
		// the intercepted attestation must not redirect the payout to the
		// attacker.
		iaPriv := createEphemeralTrustedIA(t, cfg)
		connectionKey := newTestConnectionKey(t)
		realClaimantPriv := newTestPrivkey(t)
		realClaimantPub := mustPubkey(t, realClaimantPriv)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "connection_key", IdentityValue: connectionKey, IAPubkey: mustPubkey(t, iaPriv), AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		attestation := buildIAAttestationEvent(t, iaPriv, connectionKey, realClaimantPub, time.Hour)

		attackerPriv := newTestPrivkey(t)
		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration attestation-theft test (attacker)")
		attackerProof := buildClaimProofEvent(t, attackerPriv, created.WalletPubkey, invoice.PaymentHash,
			nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())

		var attackerResult ClaimFundsResult
		err := shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:          invoice.Invoice,
			IdentityType:     "connection_key",
			IdentityValue:    connectionKey,
			IdentityEvent:    eventJSON(t, attackerProof),
			AttestationEvent: eventJSON(t, attestation),
		}, &attackerResult)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)

		// The real claimant's own claim, using the same attestation, must still succeed.
		realInvoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration attestation-theft test (real claimant)")
		realProof := buildClaimProofEvent(t, realClaimantPriv, created.WalletPubkey, realInvoice.PaymentHash,
			nostr.Tags{{"connection_key", connectionKey}, {"e", attestation.ID}}, time.Now())
		var realResult ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:          realInvoice.Invoice,
			IdentityType:     "connection_key",
			IdentityValue:    connectionKey,
			IdentityEvent:    eventJSON(t, realProof),
			AttestationEvent: eventJSON(t, attestation),
		}, &realResult))
		require.NotEmpty(t, realResult.Preimage)
	})

	t.Run("ConnectionKeyMode_AttestationExpired_Rejected", func(t *testing.T) {
		// The attestation's own expiration (distinct from the claim proof's
		// separate 5-minute freshness window) must be honored — an attestation
		// that has genuinely lapsed can't be used even though everything else
		// about it (signature, d-tag, p-tag) is perfectly valid.
		iaPriv := createEphemeralTrustedIA(t, cfg)
		connectionKey := newTestConnectionKey(t)
		claimantPriv := newTestPrivkey(t)
		claimantPub := mustPubkey(t, claimantPriv)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "connection_key", IdentityValue: connectionKey, IAPubkey: mustPubkey(t, iaPriv), AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		// Signed with an expiration timestamp already in the past.
		expiredAttestation := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, -time.Hour)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration expired-attestation test")
		proof := buildClaimProofEvent(t, claimantPriv, created.WalletPubkey, invoice.PaymentHash,
			nostr.Tags{{"connection_key", connectionKey}, {"e", expiredAttestation.ID}}, time.Now())

		var result ClaimFundsResult
		err := shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:          invoice.Invoice,
			IdentityType:     "connection_key",
			IdentityValue:    connectionKey,
			IdentityEvent:    eventJSON(t, proof),
			AttestationEvent: eventJSON(t, expiredAttestation),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)

		// A fresh attestation for the same slice must still succeed afterward.
		freshAttestation := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, time.Hour)
		freshProof := buildClaimProofEvent(t, claimantPriv, created.WalletPubkey, invoice.PaymentHash,
			nostr.Tags{{"connection_key", connectionKey}, {"e", freshAttestation.ID}}, time.Now())
		var retryResult ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:          invoice.Invoice,
			IdentityType:     "connection_key",
			IdentityValue:    connectionKey,
			IdentityEvent:    eventJSON(t, freshProof),
			AttestationEvent: eventJSON(t, freshAttestation),
		}, &retryResult))
		require.NotEmpty(t, retryResult.Preimage)
	})

	t.Run("ConnectionKeyMode_AttestationMissingExpirationTag_Rejected", func(t *testing.T) {
		// An attestation with no expiration tag at all must be rejected, not
		// treated as eternally valid. This codebase's trust model only
		// supports revoking an Identity Authority as a whole (no per-
		// attestation NIP-09 revocation against a relay), so an attestation's
		// own expiration is the only bound on how long a single mistaken or
		// compromised attestation stays honorable - an unbounded one would
		// defeat that entirely.
		iaPriv := createEphemeralTrustedIA(t, cfg)
		connectionKey := newTestConnectionKey(t)
		claimantPriv := newTestPrivkey(t)
		claimantPub := mustPubkey(t, claimantPriv)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "connection_key", IdentityValue: connectionKey, IAPubkey: mustPubkey(t, iaPriv), AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		noExpiryAttestation := &nostr.Event{
			Kind:      nostrKindIAAttestation,
			CreatedAt: nostr.Now(),
			Tags:      nostr.Tags{{"d", connectionKey}, {"p", claimantPub}},
		}
		require.NoError(t, noExpiryAttestation.Sign(iaPriv))

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration no-expiration-attestation test")
		proof := buildClaimProofEvent(t, claimantPriv, created.WalletPubkey, invoice.PaymentHash,
			nostr.Tags{{"connection_key", connectionKey}, {"e", noExpiryAttestation.ID}}, time.Now())

		var result ClaimFundsResult
		err := shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:          invoice.Invoice,
			IdentityType:     "connection_key",
			IdentityValue:    connectionKey,
			IdentityEvent:    eventJSON(t, proof),
			AttestationEvent: eventJSON(t, noExpiryAttestation),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("ConnectionKeyMode_AttestationSignedByImposterIA_Rejected", func(t *testing.T) {
		// The wallet's slice records the trusted IA pubkey it was created
		// with. A "fake" attestation here means: a real, validly-signed
		// kind-35522 event, correct d-tag/p-tag/expiration - EXCEPT it's
		// signed by a completely different key than the IA actually recorded
		// for this slice. Anyone can generate a keypair and sign a
		// well-formed attestation for themselves; it must still be rejected
		// because it doesn't match the specific IA this wallet trusted at
		// creation time.
		iaPriv := createEphemeralTrustedIA(t, cfg)
		connectionKey := newTestConnectionKey(t)
		claimantPriv := newTestPrivkey(t)
		claimantPub := mustPubkey(t, claimantPriv)
		imposterIAPriv := newTestPrivkey(t) // a real keypair, but not this slice's recorded IA

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "connection_key", IdentityValue: connectionKey, IAPubkey: mustPubkey(t, iaPriv), AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		imposterAttestation := buildIAAttestationEvent(t, imposterIAPriv, connectionKey, claimantPub, time.Hour)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration imposter-IA-attestation test")
		proof := buildClaimProofEvent(t, claimantPriv, created.WalletPubkey, invoice.PaymentHash,
			nostr.Tags{{"connection_key", connectionKey}, {"e", imposterAttestation.ID}}, time.Now())

		var result ClaimFundsResult
		err := shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:          invoice.Invoice,
			IdentityType:     "connection_key",
			IdentityValue:    connectionKey,
			IdentityEvent:    eventJSON(t, proof),
			AttestationEvent: eventJSON(t, imposterAttestation),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("ConnectionKeyMode_AttestationForgedSignature_Rejected", func(t *testing.T) {
		// A structurally perfect attestation (correct pubkey field claiming to
		// be the trusted IA, correct d-tag, correct p-tag, correct expiration)
		// but whose signature was never actually produced by the IA's private
		// key - simulating an attacker who knows the IA's public key (public
		// by definition) and tries to fabricate an attestation without
		// possessing the corresponding secret.
		iaPriv := createEphemeralTrustedIA(t, cfg)
		iaPub := mustPubkey(t, iaPriv)
		connectionKey := newTestConnectionKey(t)
		claimantPriv := newTestPrivkey(t)
		claimantPub := mustPubkey(t, claimantPriv)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: []JITWalletRecipientParam{
				{IdentityType: "connection_key", IdentityValue: connectionKey, IAPubkey: iaPub, AmountMloki: happyPathAmountMloki},
			},
			Expiry: happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		forged := buildIAAttestationEvent(t, iaPriv, connectionKey, claimantPub, time.Hour)
		// Claims to be signed by the real IA pubkey, but the signature itself
		// is garbage - never actually produced by the IA's private key.
		forged.Sig = strings.Repeat("00", 64)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration forged-attestation test")
		proof := buildClaimProofEvent(t, claimantPriv, created.WalletPubkey, invoice.PaymentHash,
			nostr.Tags{{"connection_key", connectionKey}, {"e", forged.ID}}, time.Now())

		var result ClaimFundsResult
		err := shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:          invoice.Invoice,
			IdentityType:     "connection_key",
			IdentityValue:    connectionKey,
			IdentityEvent:    eventJSON(t, proof),
			AttestationEvent: eventJSON(t, forged),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)
	})

	t.Run("StaleClaimProof_Rejected", func(t *testing.T) {
		beneficiaryPriv := newTestPrivkey(t)
		beneficiaryPub := mustPubkey(t, beneficiaryPriv)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration stale-proof test")

		// Signed well outside jitClaimIdentityFreshnessWindow (5 minutes) —
		// defense-in-depth on top of the invoice/wallet binding, so even a
		// correctly-bound proof must be rejected once it's stale.
		staleProof := buildClaimProofEvent(t, beneficiaryPriv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now().Add(-10*time.Minute))
		var result ClaimFundsResult
		err := shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: beneficiaryPub,
			IdentityEvent: eventJSON(t, staleProof),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_BAD_REQUEST)

		// The slice must remain claimable with a fresh proof against the same
		// invoice afterward.
		freshProof := buildClaimProofEvent(t, beneficiaryPriv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now())
		var retryResult ClaimFundsResult
		require.NoError(t, shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: beneficiaryPub,
			IdentityEvent: eventJSON(t, freshProof),
		}, &retryResult))
		require.NotEmpty(t, retryResult.Preimage)
	})

	t.Run("RateLimiting_TwentyFirstClaimIsRateLimited", func(t *testing.T) {
		skipIfEnvUnset(t, "INTEGRATION_RUN_RATE_LIMIT_TESTS")

		beneficiaryPub, err := nostr.GetPublicKey(newTestPrivkey(t))
		require.NoError(t, err)
		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     happyPathExpirySecs,
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		// claim_funds' rate limiter (per calling connection) is checked
		// before any param validation — see claim_funds_controller.go step
		// 2 — so deliberately empty/invalid calls are enough to exhaust it
		// without minting a real invoice for every attempt.
		var lastErr error
		for i := 0; i < 21; i++ {
			var result ClaimFundsResult
			lastErr = shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{}, &result)
			if lastErr != nil {
				t.Logf("claim #%d/21: error: %v", i+1, lastErr)
			}
		}
		requireNWCErrorCode(t, lastErr, constants.ERROR_RATE_LIMITED)
	})

	t.Run("WalletExpired_ClaimRejected", func(t *testing.T) {
		// A wallet's own ExpiresAt (distinct from a claim proof's own 5-minute
		// freshness window, see StaleClaimProof_Rejected above) is enforced via
		// the generic scope-permission gate in nip47/event_handler.go, not by
		// claim_funds_controller.go itself. A short-lived real wallet proves
		// that gate actually fires end to end, well before the 5-minute
		// background cleanup ticker (service/jit_cleanup_service.go) would ever
		// sweep it — so this is genuinely exercising the permission check, not
		// racing wallet deletion.
		beneficiaryPriv := newTestPrivkey(t)
		beneficiaryPub := mustPubkey(t, beneficiaryPriv)

		var created CreateJITWalletResult
		require.NoError(t, hubClient.Call(ctxT(t), constants.NIP47MethodCreateJITWallet, CreateJITWalletParams{
			Recipients: onePubkeyRecipient(beneficiaryPub, happyPathAmountMloki),
			Expiry:     2, // seconds - deliberately short so the wallet expires mid-test
		}, &created))
		shared := mustConnect(t, created.PairingURI)

		time.Sleep(3 * time.Second)

		invoice := mintInvoiceFromSimpleWallet(t, cfg, happyPathAmountMloki, "integration wallet-expired claim test")
		proof := buildClaimProofEvent(t, beneficiaryPriv, created.WalletPubkey, invoice.PaymentHash, nil, time.Now())
		var result ClaimFundsResult
		err := shared.Call(ctxT(t), constants.NIP47MethodClaimFunds, ClaimFundsParams{
			Invoice:       invoice.Invoice,
			IdentityType:  "pubkey",
			IdentityValue: beneficiaryPub,
			IdentityEvent: eventJSON(t, proof),
		}, &result)
		requireNWCErrorCode(t, err, constants.ERROR_EXPIRED)
	})
}
