package lokicash

// Financial / economic design review — mint provenance amount attestation
// (2026-08-29 round). Reuses signMintPayload/minterKey/testPubkey from the
// existing lokicash test files (same package). No existing file is modified.
//
// These tests pin what a recipient can and CANNOT economically rely on when a
// token carries a mint signature (TLV 5) + attested amount (TLV 6).

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCashAuditProvenance_AttestedAmountIsMintTimeSnapshot_NotLiveValue proves
// the attested amount is a purely offline, mint-time claim: VerifyMint recovers
// (signer, amount) from the token BYTES ALONE, with no reference to any ledger,
// balance, claim state, or custody. The identical token verifies to the identical
// full amount whether the underlying slice is still worth that much or has since
// been redeemed / split / consolidated to zero on the node.
//
// Economic consequence: a recipient handed such a token (a bearer lokicash most
// acutely, where possession is meant to equal value) can verify origin and
// denomination offline, but the attested amount can be economically STALE — the
// token cannot signal that its value was already collected. This matches the
// spec's own "Provenance is not custody" caveat, so it is Informational, but it
// is a real reliance boundary worth stating for the consolidate/split flows,
// each of which mints a fresh attested token over a fresh amount.
func TestCashAuditProvenance_AttestedAmountIsMintTimeSnapshot_NotLiveValue(t *testing.T) {
	priv, minter := minterKey(t)
	const attested = uint64(50_000)

	sig := signMintPayload(t, priv, HRP, testPubkey(), attested)
	amt := attested
	tok := Token{
		HRP:            HRP,
		WalletPubkey:   testPubkey(),
		Secret:         testSecret(),
		MintSignature:  sig,
		AttestedAmount: &amt,
	}

	// VerifyMint's whole input is the token; it consults nothing else.
	recovered, ok := VerifyMint(tok)
	require.True(t, ok, "a well-formed provenance pair must verify")
	require.Equal(t, minter, recovered, "recovers the minter offline")
	require.NotNil(t, tok.AttestedAmount)
	require.Equal(t, attested, *tok.AttestedAmount,
		"attested amount is whatever was signed at mint time — there is no live-value component to it")

	// The same bytes verify identically no matter the (hypothetical) live state:
	// VerifyMint is a pure function of the Token, so a drained/redeemed wallet's
	// token still recovers the full attested amount. Re-verifying the SAME token
	// twice is the strongest thing a unit test can assert about a function that,
	// by construction, cannot observe custody — and that inability IS the point.
	recovered2, ok2 := VerifyMint(tok)
	require.True(t, ok2)
	require.Equal(t, recovered, recovered2)
	require.Equal(t, attested, *tok.AttestedAmount,
		"nothing about collecting/splitting the underlying slice can change what this token attests")
}

// TestCashAuditProvenance_OneMinterCanAttestArbitraryAmounts reinforces that the
// attestation binds (minter, wallet_pubkey, amount) but says nothing about
// whether the minter ever actually funded that wallet for that amount, or still
// custodies it. The same minter key produces equally-valid provenance for two
// different amounts over the same wallet pubkey — so "the signature verifies" is
// strictly a statement about origin, never about redeemable value. A client MUST
// still let a real cash_redeem / cash_consolidate call be the authority.
func TestCashAuditProvenance_OneMinterCanAttestArbitraryAmounts(t *testing.T) {
	priv, minter := minterKey(t)
	wallet := testPubkey()

	for _, amount := range []uint64{1, 25_000, 1_000_000_000} {
		amt := amount
		tok := Token{
			HRP:            HRP,
			WalletPubkey:   wallet,
			Secret:         testSecret(),
			MintSignature:  signMintPayload(t, priv, HRP, wallet, amount),
			AttestedAmount: &amt,
		}
		recovered, ok := VerifyMint(tok)
		require.True(t, ok, "amount=%d must verify", amount)
		require.Equal(t, minter, recovered,
			"every amount recovers the same minter — the amount is attested, not proven-funded (amount=%d)", amount)
	}
}
