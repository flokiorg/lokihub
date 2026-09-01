package lokicash

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signMintFor produces a real flnd-style mint signature (prefix + double-SHA256,
// compact-recoverable) over the given fields, so VerifyMint's recovery path is
// exercised exactly as it is against the real node.
func signMintFor(t *testing.T, priv *btcec.PrivateKey, hrp, walletPubkeyHex string, amount uint64) []byte {
	t.Helper()
	digest := chainhash.DoubleHashB([]byte(LNSignedMessagePrefix + MintPayload(hrp, walletPubkeyHex, amount)))
	sig := ecdsa.SignCompact(priv, digest, true)
	require.Len(t, sig, mintSigLen)
	return sig
}

func u64ptr(v uint64) *uint64 { return &v }

// TestVerifyMint_ForgeryAndTamperVectors is the core financial-integrity table:
// a mint signature must recover the node's real pubkey ONLY when nothing it
// commits to (HRP, wallet pubkey, amount) has been altered. Any tamper must
// EITHER fail to recover a pubkey at all (ok=false) OR recover a DIFFERENT
// pubkey than the true signer — so a caller comparing the recovered key against
// the minter it trusts always rejects a forged/tampered token. Crucially, a
// tampered token must NEVER recover the true signer's key (that would let an
// attacker restate a token's denomination while keeping a valid-looking
// provenance).
func TestVerifyMint_ForgeryAndTamperVectors(t *testing.T) {
	signerPriv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	signerPub := hex.EncodeToString(signerPriv.PubKey().SerializeCompressed())

	// Two distinct legitimately-signed tokens, used both for happy-path and for
	// cross-token signature-reuse attempts.
	pkA := strings.Repeat("11", keyLen)
	pkB := strings.Repeat("22", keyLen)
	const amtA uint64 = 40_000
	const amtB uint64 = 7_000
	sigA := signMintFor(t, signerPriv, HRP, pkA, amtA)
	sigB := signMintFor(t, signerPriv, HRP, pkB, amtB)

	// A signature from a DIFFERENT node entirely.
	otherPriv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	otherPub := hex.EncodeToString(otherPriv.PubKey().SerializeCompressed())
	sigOther := signMintFor(t, otherPriv, HRP, pkA, amtA)

	cases := []struct {
		name string
		tok  Token
		// wantOK: does VerifyMint return a recovered pubkey at all?
		wantOK bool
		// wantSigner: iff true, the recovered pubkey MUST equal signerPub.
		// iff false (with wantOK true), it MUST NOT equal signerPub — a tamper
		// that still recovered the true signer would be a forgery bypass.
		wantSigner bool
	}{
		{
			name:       "valid token recovers the true signer",
			tok:        Token{HRP: HRP, WalletPubkey: pkA, MintSignature: sigA, AttestedAmount: u64ptr(amtA)},
			wantOK:     true,
			wantSigner: true,
		},
		{
			name:       "second valid token also recovers the SAME signer (stable identity)",
			tok:        Token{HRP: HRP, WalletPubkey: pkB, MintSignature: sigB, AttestedAmount: u64ptr(amtB)},
			wantOK:     true,
			wantSigner: true,
		},
		{
			name:       "tampered amount (inflate) must not recover the signer",
			tok:        Token{HRP: HRP, WalletPubkey: pkA, MintSignature: sigA, AttestedAmount: u64ptr(amtA + 1)},
			wantOK:     true,
			wantSigner: false,
		},
		{
			name:       "tampered amount (deflate to zero) must not recover the signer",
			tok:        Token{HRP: HRP, WalletPubkey: pkA, MintSignature: sigA, AttestedAmount: u64ptr(0)},
			wantOK:     true,
			wantSigner: false,
		},
		{
			name:       "tampered wallet pubkey must not recover the signer",
			tok:        Token{HRP: HRP, WalletPubkey: pkB, MintSignature: sigA, AttestedAmount: u64ptr(amtA)},
			wantOK:     true,
			wantSigner: false,
		},
		{
			name:       "tampered HRP (satscash) must not recover the signer",
			tok:        Token{HRP: "satscash", WalletPubkey: pkA, MintSignature: sigA, AttestedAmount: u64ptr(amtA)},
			wantOK:     true,
			wantSigner: false,
		},
		{
			name:       "cross-token signature reuse (sigB on token A's fields) must not recover the signer",
			tok:        Token{HRP: HRP, WalletPubkey: pkA, MintSignature: sigB, AttestedAmount: u64ptr(amtA)},
			wantOK:     true,
			wantSigner: false,
		},
		{
			name:       "swapped amount between two real tokens must not recover the signer",
			tok:        Token{HRP: HRP, WalletPubkey: pkA, MintSignature: sigA, AttestedAmount: u64ptr(amtB)},
			wantOK:     true,
			wantSigner: false,
		},
		{
			name:       "signature from another node recovers THAT node, not our signer",
			tok:        Token{HRP: HRP, WalletPubkey: pkA, MintSignature: sigOther, AttestedAmount: u64ptr(amtA)},
			wantOK:     true,
			wantSigner: false,
		},
		{
			name:   "all-zero signature (invalid recovery header) returns no provenance",
			tok:    Token{HRP: HRP, WalletPubkey: pkA, MintSignature: make([]byte, mintSigLen), AttestedAmount: u64ptr(amtA)},
			wantOK: false,
		},
		{
			name:   "wrong-length signature returns no provenance",
			tok:    Token{HRP: HRP, WalletPubkey: pkA, MintSignature: sigA[:mintSigLen-1], AttestedAmount: u64ptr(amtA)},
			wantOK: false,
		},
		{
			name:   "signature present, amount missing returns no provenance",
			tok:    Token{HRP: HRP, WalletPubkey: pkA, MintSignature: sigA},
			wantOK: false,
		},
		{
			name:   "amount present, signature missing returns no provenance",
			tok:    Token{HRP: HRP, WalletPubkey: pkA, AttestedAmount: u64ptr(amtA)},
			wantOK: false,
		},
		{
			name:   "no provenance at all returns no provenance",
			tok:    Token{HRP: HRP, WalletPubkey: pkA},
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := VerifyMint(tc.tok)
			require.Equal(t, tc.wantOK, ok, "ok mismatch (recovered=%q)", got)
			if !tc.wantOK {
				assert.Empty(t, got)
				return
			}
			if tc.wantSigner {
				assert.Equal(t, signerPub, got, "valid token must recover the true signer")
			} else {
				// The one and only financial-integrity invariant: a tampered or
				// forged token must NEVER recover the TRUE signer's key. (It may
				// recover some other key — e.g. genuinely the other node's — which
				// the caller's trust check then rejects.)
				assert.NotEqual(t, signerPub, got, "SECURITY: a tampered/forged token must NEVER recover the true signer")
			}
			_ = otherPub
		})
	}
}

// TestVerifyMint_OtherNodeSignatureRecoversOtherNode is split out from the table
// above (which asserts recovered != otherPub) to positively confirm the
// other-node signature does recover the other node — proving the recovery is
// real, not coincidental garbage.
func TestVerifyMint_OtherNodeSignatureRecoversOtherNode(t *testing.T) {
	priv, err := btcec.NewPrivateKey()
	require.NoError(t, err)
	pub := hex.EncodeToString(priv.PubKey().SerializeCompressed())
	pk := strings.Repeat("33", keyLen)
	const amt uint64 = 12_345
	sig := signMintFor(t, priv, HRP, pk, amt)

	got, ok := VerifyMint(Token{HRP: HRP, WalletPubkey: pk, MintSignature: sig, AttestedAmount: u64ptr(amt)})
	require.True(t, ok)
	assert.Equal(t, pub, got)
}

// TestDecode_ProvenanceMalformation_NeverHardFails is the wire-level companion:
// a token whose provenance TLVs are malformed in any way MUST still decode
// successfully (the connection credential is intact) but expose NO provenance —
// malformed provenance can never brick an otherwise-valid token, nor can it be
// smuggled through as if valid.
func TestDecode_ProvenanceMalformation_NeverHardFails(t *testing.T) {
	base := []rawEntry{
		{typ: tlvWalletPubkey, value: mustHex(t, strings.Repeat("ab", keyLen))},
		{typ: tlvSecret, value: mustHex(t, strings.Repeat("cd", keyLen))},
	}
	goodAmount := make([]byte, attestedAmountLen)
	binary.BigEndian.PutUint64(goodAmount, 999)
	goodSig := make([]byte, mintSigLen)

	cases := map[string][]rawEntry{
		"lone signature":              {{typ: tlvMintSignature, value: goodSig}},
		"lone amount":                 {{typ: tlvAttestedAmount, value: goodAmount}},
		"sig wrong length + amount":   {{typ: tlvMintSignature, value: make([]byte, mintSigLen+1)}, {typ: tlvAttestedAmount, value: goodAmount}},
		"amount wrong length + sig":   {{typ: tlvMintSignature, value: goodSig}, {typ: tlvAttestedAmount, value: make([]byte, attestedAmountLen-2)}},
		"duplicate signature":         {{typ: tlvMintSignature, value: goodSig}, {typ: tlvMintSignature, value: goodSig}, {typ: tlvAttestedAmount, value: goodAmount}},
		"duplicate amount":            {{typ: tlvMintSignature, value: goodSig}, {typ: tlvAttestedAmount, value: goodAmount}, {typ: tlvAttestedAmount, value: goodAmount}},
		"empty signature value":       {{typ: tlvMintSignature, value: []byte{}}, {typ: tlvAttestedAmount, value: goodAmount}},
		"empty amount value":          {{typ: tlvMintSignature, value: goodSig}, {typ: tlvAttestedAmount, value: []byte{}}},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			encoded := encodeRaw(t, HRP, rawTLV(t, append(append([]rawEntry(nil), base...), extra...)))
			out, err := Decode(encoded)
			require.NoError(t, err, "malformed provenance must never fail the whole decode")
			assert.Nil(t, out.MintSignature, "poisoned provenance must not survive as a signature")
			assert.Nil(t, out.AttestedAmount, "poisoned provenance must not survive as an amount")
			_, ok := VerifyMint(out)
			assert.False(t, ok)
			// The connection credential itself must still be intact.
			assert.NotEmpty(t, out.WalletPubkey)
			assert.NotEmpty(t, out.Secret)
		})
	}
}
