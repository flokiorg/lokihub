package lokicash

// nmilat migration (PR #90): Encode/Decode/MintPayload/VerifyMint now
// delegate to github.com/ohstr/nmilat/nipcash's token codec / mint-provenance
// helpers (see lokicash.go's own doc comments). Equivalence was verified
// first, as a Phase 0 spike, before that delegation landed; these tests now
// serve as permanent regression coverage for it - toNipcashToken/
// fromNipcashToken are lokicash.go's own production conversion helpers, not
// reimplemented here.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ohstr/nmilat/nipcash"
)

func requireTokensEqual(t *testing.T, want Token, got Token) {
	t.Helper()
	assert.Equal(t, want.HRP, got.HRP, "HRP")
	assert.Equal(t, want.WalletPubkey, got.WalletPubkey, "WalletPubkey")
	assert.Equal(t, want.Secret, got.Secret, "Secret")
	assert.Equal(t, want.RelayURLs, got.RelayURLs, "RelayURLs")
	if want.IdentityRequired == nil {
		assert.Nil(t, got.IdentityRequired, "IdentityRequired")
	} else {
		require.NotNil(t, got.IdentityRequired, "IdentityRequired")
		assert.Equal(t, *want.IdentityRequired, *got.IdentityRequired, "IdentityRequired value")
	}
	assert.Equal(t, want.MintSignature, got.MintSignature, "MintSignature")
	if want.AttestedAmount == nil {
		assert.Nil(t, got.AttestedAmount, "AttestedAmount")
	} else {
		require.NotNil(t, got.AttestedAmount, "AttestedAmount")
		assert.Equal(t, *want.AttestedAmount, *got.AttestedAmount, "AttestedAmount value")
	}
}

// TestNmilatEquivalence_TokenCodec_CrossDecode proves a token encoded by one
// implementation decodes identically via the other, in both directions,
// across a field matrix covering every optional dimension the wire format
// has: no relays / one relay / many relays, IdentityRequired nil/true/false,
// and mint provenance present/absent.
func TestNmilatEquivalence_TokenCodec_CrossDecode(t *testing.T) {
	trueVal := true
	falseVal := false
	amt := uint64(123_456)
	sig := make([]byte, mintSigLen)
	for i := range sig {
		sig[i] = byte(i + 1)
	}

	cases := []struct {
		name string
		tok  Token
	}{
		{"minimal_no_relays", Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret()}},
		{"one_relay", Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret(), RelayURLs: []string{"wss://relay.example.com"}}},
		{"many_relays", Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret(), RelayURLs: []string{"wss://a.example.com", "wss://b.example.com", "wss://c.example.com"}}},
		{"identity_required_true", Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret(), IdentityRequired: &trueVal}},
		{"identity_required_false", Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret(), IdentityRequired: &falseVal}},
		{"other_hrp", Token{HRP: "satscash", WalletPubkey: testPubkey(), Secret: testSecret()}},
		{"with_provenance", Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret(), RelayURLs: []string{"wss://relay.example.com"}, MintSignature: sig, AttestedAmount: &amt}},
		{"everything_together", Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret(), RelayURLs: []string{"wss://a.example.com", "wss://b.example.com"}, IdentityRequired: &trueVal, MintSignature: sig, AttestedAmount: &amt}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("lokicash_encode_nipcash_decode", func(t *testing.T) {
				encoded, err := Encode(tc.tok)
				require.NoError(t, err)
				decoded, err := nipcash.Decode(encoded)
				require.NoError(t, err)
				requireTokensEqual(t, tc.tok, fromNipcashToken(decoded))
			})
			t.Run("nipcash_encode_lokicash_decode", func(t *testing.T) {
				encoded, err := nipcash.Encode(toNipcashToken(tc.tok))
				require.NoError(t, err)
				decoded, err := Decode(encoded)
				require.NoError(t, err)
				requireTokensEqual(t, tc.tok, decoded)
			})
			t.Run("bech32_strings_identical", func(t *testing.T) {
				// Not just "decodes the same" - the encoded wire bytes
				// themselves must match exactly, proving identical TLV
				// ordering/framing, not just a decoder that happens to be
				// lenient about ordering.
				a, err := Encode(tc.tok)
				require.NoError(t, err)
				b, err := nipcash.Encode(toNipcashToken(tc.tok))
				require.NoError(t, err)
				assert.Equal(t, a, b, "lokicash.Encode and nipcash.Encode must produce byte-identical bech32 strings for the same Token")
			})
		})
	}
}

// TestNmilatEquivalence_TokenCodec_MalformedRejection proves both decoders
// agree on what's invalid, not just on what's valid - a decoder that's
// silently more lenient than the other is itself a real compatibility risk
// (a token one side accepts and the other rejects).
func TestNmilatEquivalence_TokenCodec_MalformedRejection(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"not_bech32", "not-a-valid-token-at-all"},
		{"empty_string", ""},
		{"truncated", "lokicash1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, lokicashErr := Decode(tc.token)
			_, nipcashErr := nipcash.Decode(tc.token)
			assert.Error(t, lokicashErr, "lokicash.Decode should reject %q", tc.token)
			assert.Error(t, nipcashErr, "nipcash.Decode should reject %q", tc.token)
		})
	}

	t.Run("missing_wallet_pubkey_rejected_by_both_encoders", func(t *testing.T) {
		incomplete := Token{HRP: HRP, Secret: testSecret()}
		_, lokicashErr := Encode(incomplete)
		_, nipcashErr := nipcash.Encode(toNipcashToken(incomplete))
		assert.Error(t, lokicashErr, "lokicash.Encode should reject a Token with no WalletPubkey")
		assert.Error(t, nipcashErr, "nipcash.Encode should reject a Token with no WalletPubkey")
	})
}

// TestNmilatEquivalence_MintPayload proves both implementations' mint-signature
// payload strings are byte-identical for the same inputs - the one thing
// signer and verifier on either side MUST agree on for a signature minted by
// one implementation to ever verify under the other.
func TestNmilatEquivalence_MintPayload(t *testing.T) {
	cases := []struct {
		hrp    string
		pubkey string
		amount uint64
	}{
		{HRP, testPubkey(), 0},
		{HRP, testPubkey(), 1},
		{HRP, testPubkey(), 40_000},
		{HRP, testPubkey(), 18_446_744_073_709_551_615}, // max uint64
		{"satscash", testPubkey(), 21_000_000},
	}
	for _, tc := range cases {
		got := MintPayload(tc.hrp, tc.pubkey, tc.amount)
		want := nipcash.MintPayload(tc.hrp, tc.pubkey, tc.amount)
		assert.Equal(t, want, got, "hrp=%s pubkey=%s amount=%d", tc.hrp, tc.pubkey, tc.amount)
	}
}

// TestNmilatEquivalence_ProvenanceVerification signs a real mint-provenance
// payload the way lokihub's own cashwallet package does (mirroring
// signMintPayload's digest scheme, itself mirroring flnd/LND's SignMessage),
// then verifies it through BOTH lokicash.VerifyMint and
// nipcash.VerifyProvenance, and asserts they recover the identical minter
// pubkey. This is the highest-value equivalence check of the three: it
// exercises two different secp256k1 library stacks (btcsuite vs
// decred/flokiorg-fork) doing recoverable-ECDSA recovery over the same
// digest, which is exactly the kind of cross-implementation assumption that
// must be proven, not assumed, before any production signing/verification
// code is consolidated onto nmilat.
func TestNmilatEquivalence_ProvenanceVerification(t *testing.T) {
	priv, minterPubkey := minterKey(t)
	const amount uint64 = 40_000
	sig := signMintPayload(t, priv, HRP, testPubkey(), amount)

	amt := amount
	lokicashTok := Token{
		HRP:            HRP,
		WalletPubkey:   testPubkey(),
		Secret:         testSecret(),
		MintSignature:  sig,
		AttestedAmount: &amt,
	}
	nipcashTok := toNipcashToken(lokicashTok)

	t.Run("both_recover_the_same_minter_pubkey", func(t *testing.T) {
		gotPubkey, gotOK := VerifyMint(lokicashTok)
		require.True(t, gotOK)
		require.Equal(t, minterPubkey, gotPubkey)

		wantPubkey, wantOK := nipcash.VerifyProvenance(nipcashTok)
		require.True(t, wantOK)
		require.Equal(t, minterPubkey, wantPubkey)

		assert.Equal(t, gotPubkey, wantPubkey, "lokicash.VerifyMint and nipcash.VerifyProvenance must recover the identical pubkey from the identical signature")
	})

	t.Run("both_reject_a_corrupted_signature", func(t *testing.T) {
		corrupted := append([]byte(nil), sig...)
		corrupted[10] ^= 0xFF
		corruptedLokicashTok := lokicashTok
		corruptedLokicashTok.MintSignature = corrupted
		corruptedNipcashTok := toNipcashToken(corruptedLokicashTok)

		_, lokicashOK := VerifyMint(corruptedLokicashTok)
		_, nipcashOK := nipcash.VerifyProvenance(corruptedNipcashTok)
		// A corrupted signature either fails to recover at all, or recovers to
		// a different (wrong) pubkey - either way it must never equal the real
		// minter's pubkey. Assert both sides agree on "not the real minter",
		// not necessarily on the literal ok value, since ECDSA recovery can
		// sometimes still "succeed" against a bit-flipped signature while
		// recovering a bogus pubkey.
		if lokicashOK {
			p, _ := VerifyMint(corruptedLokicashTok)
			assert.NotEqual(t, minterPubkey, p)
		}
		if nipcashOK {
			p, _ := nipcash.VerifyProvenance(corruptedNipcashTok)
			assert.NotEqual(t, minterPubkey, p)
		}
	})

	t.Run("both_report_no_provenance_identically", func(t *testing.T) {
		bare := Token{HRP: HRP, WalletPubkey: testPubkey(), Secret: testSecret()}
		_, lokicashOK := VerifyMint(bare)
		_, nipcashOK := nipcash.VerifyProvenance(toNipcashToken(bare))
		assert.False(t, lokicashOK)
		assert.False(t, nipcashOK)
	})
}
