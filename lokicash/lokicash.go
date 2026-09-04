// Package lokicash implements the lokicash1... bech32 token that packages a
// Cash wallet's NWC pairing data (NIP-JW §The Lokicash Token) as one
// shareable string, NIP-19-style. The TLV layout is shared by any
// energy-backed coin's variant of this token — only the bech32
// human-readable prefix changes (lokicash for flokicoin, satscash for a
// Bitcoin-backed equivalent, etc.); Decode accepts any prefix and returns it
// to the caller rather than assuming HRP.
package lokicash

import (
	"bytes"

	"github.com/ohstr/nmilat/nipcash"
)

// HRP is the bech32 human-readable prefix lokihub issues its own tokens
// under.
const HRP = "lokicash"

// TLV type numbers within a lokicash-family token. 0 and 1 follow the same
// convention NIP-19 uses for nprofile/nevent/naddr (0 is the token's
// primary identifier, 1 is a relay hint); 2 is specific to this token
// family. 3 is an optional metadata hint, added after the format's initial
// release — decoders written before it existed still parse everything else
// correctly (the "unknown TLV type: ignore" rule below is exactly what makes
// that safe). 4 (tlvMaxTransfers) was retired along with the max_transfers
// feature itself and MUST NOT be reused for a new meaning — an old token
// still carrying it decodes fine (ignored as an unknown type). 5 and 6 are the
// optional mint-provenance pair (NIP-CASH §Mint Provenance): a recoverable
// node signature and the amount it commits to. Both-or-neither — a token
// carrying one without the other has no valid provenance (never a decode
// failure, since both are optional).
const (
	tlvWalletPubkey     uint8 = 0
	tlvRelay            uint8 = 1
	tlvSecret           uint8 = 2
	tlvIdentityRequired uint8 = 3
	tlvMintSignature    uint8 = 5
	tlvAttestedAmount   uint8 = 6
)

// keyLen is the byte length of both a wallet pubkey and a pairing secret —
// raw 32-byte values, same as every other Nostr key.
const keyLen = 32

// mintSigLen is the byte length of a recoverable ECDSA compact signature (1
// recovery byte + 32-byte R + 32-byte S), the raw form LND's SignMessage
// produces before zbase32 encoding. The token carries the raw bytes, not the
// zbase32 string, to stay compact.
const mintSigLen = 65

// attestedAmountLen is the fixed width of the attested-amount TLV value: an
// 8-byte big-endian millis amount.
const attestedAmountLen = 8

// LNSignedMessagePrefix is the context prefix the flnd node prepends to every
// message before double-SHA256-hashing and signing it (flnd rpcserver.go's
// signedMsgPrefix). A mint signature is produced by the node's own
// SignMessage, so VerifyMint MUST reproduce this exact prefixing to recover the
// signer — the same string flnd's own VerifyMessage uses.
const LNSignedMessagePrefix = "Flokicoin Lightning Signed Message:"

// Token is the decoded content of a lokicash-family bech32 string: the
// pieces of NIP-47 pairing data a Cash wallet connection needs (NIP-JW §The
// Pairing Connection), plus one optional metadata hint (NIP-JW §The Lokicash
// Token → Redemption Metadata).
//
// IdentityRequired is a pointer specifically so a caller can tell "this
// token doesn't carry this hint" (nil — true of any token minted before this
// field existed, or one an encoder chose not to populate) apart from a real,
// meaningful zero value. Treat it as a best-effort hint only, snapshotted at
// whatever moment the token was minted or last re-derived — NOT a live
// guarantee: the wallet's actual identity requirement can change afterward
// via cash_transfer, same as everything else about a Cash wallet connection.
// Every cash_redeem/cash_transfer call is still authoritatively checked
// server-side regardless of what a token implies; a client MUST NOT treat
// this field as a substitute for that check, only as a hint for deciding how
// to attempt one.
type Token struct {
	HRP          string   // e.g. "lokicash", "satscash"
	WalletPubkey string   // hex, 32 bytes
	Secret       string   // hex, 32 bytes — the NWC connection secret
	RelayURLs    []string // in encoded order
	// IdentityRequired: true means every slice this wallet currently serves
	// is identity-bound (cash_redeem/cash_transfer need a signed proof); false
	// means the wallet is a single bearer slice (only its secret is needed —
	// no proof, no Nostr identity at all). Always uniform across a wallet's
	// whole recipient set (NIP-JW: a bearer slice is always the wallet's
	// only one), so this is well-defined per wallet, not per slice.
	IdentityRequired *bool
	// MintSignature and AttestedAmount are the optional mint-provenance pair
	// (NIP-CASH §Mint Provenance): a recoverable ECDSA signature (raw
	// mintSigLen bytes) by the minting node's Lightning identity key over
	// MintPayload(HRP, WalletPubkey, *AttestedAmount), and the amount that
	// payload commits to. Both nil = no provenance; both set = provenance
	// present. Decode enforces both-or-neither by dropping a lone one (never a
	// decode failure — both are optional). Verify with VerifyMint, which
	// recovers the signer pubkey; a bare signature proves origin and
	// denomination but is NEVER a spending credential.
	MintSignature  []byte
	AttestedAmount *uint64
}

// Encode packages t into a lokicash-family bech32 token under t.HRP.
// WalletPubkey and Secret MUST each be a 32-byte hex string. Delegates to
// nipcash.Encode (github.com/ohstr/nmilat) — the same TLV wire format,
// verified byte-for-byte equivalent to this package's own former
// implementation in lokicash/nmilat_equivalence_test.go before this delegation
// was adopted (see PR #90's nmilat migration).
func Encode(t Token) (string, error) {
	return nipcash.Encode(toNipcashToken(t))
}

// Decode parses a lokicash-family bech32 token back into its pairing data.
// It rejects anything missing either required field (wallet pubkey or
// secret), carrying a malformed length for either, or repeating either one —
// a caller that skipped this check could otherwise hand a recipient a
// connection string for the wrong wallet, or one with a truncated or
// ambiguous secret that fails to pair, or pairs with the wrong party.
// Delegates to nipcash.Decode (github.com/ohstr/nmilat) — see Encode's own
// doc comment for why.
func Decode(token string) (Token, error) {
	nt, err := nipcash.Decode(token)
	if err != nil {
		return Token{}, err
	}
	return fromNipcashToken(nt), nil
}

// toNipcashToken/fromNipcashToken convert between this package's own Token
// (AttestedAmount, kept for backwards compatibility with every existing
// lokicash.Token{...} call site in this codebase) and nipcash.Token
// (AttestedAmountMillis) — same wire format, different Go field name only.
func toNipcashToken(t Token) nipcash.Token {
	return nipcash.Token{
		HRP:                  t.HRP,
		WalletPubkey:         t.WalletPubkey,
		Secret:               t.Secret,
		RelayURLs:            t.RelayURLs,
		IdentityRequired:     t.IdentityRequired,
		MintSignature:        t.MintSignature,
		AttestedAmountMillis: t.AttestedAmount,
	}
}

func fromNipcashToken(nt nipcash.Token) Token {
	return Token{
		HRP:              nt.HRP,
		WalletPubkey:     nt.WalletPubkey,
		Secret:           nt.Secret,
		RelayURLs:        nt.RelayURLs,
		IdentityRequired: nt.IdentityRequired,
		MintSignature:    nt.MintSignature,
		AttestedAmount:   nt.AttestedAmountMillis,
	}
}

// MintPayload returns the canonical ASCII string a mint signature commits to
// (NIP-CASH §Mint Provenance): "lokicash-mint:v1:<hrp>:<wallet_pubkey_hex>:<amount_millis>".
// The minter signs this at mint time and a verifier recomputes it from a
// token's own fields — so both the signer (cashwallet) and VerifyMint MUST use
// this one function, never hand-build the string, to stay byte-identical.
// Delegates to nipcash.MintPayload — see Encode's own doc comment for why.
func MintPayload(hrp, walletPubkeyHex string, amountMillis uint64) string {
	return nipcash.MintPayload(hrp, walletPubkeyHex, amountMillis)
}

// VerifyMint recovers the minting node's Lightning pubkey (compressed, hex)
// from a token's mint-provenance pair, verifying it against the token's own
// payload. It returns the recovered pubkey and true only when the token
// carries a complete, well-formed pair whose signature recovers cleanly;
// otherwise it returns "", false — for a token with no provenance at all, or
// one whose signature doesn't verify.
//
// VerifyMint does NOT decide trust: it answers "who signed this," not "should
// I trust them." The caller compares the returned pubkey against whichever
// minter it expects (e.g. its own node's pubkey for a consolidation
// pre-check, or a trusted-minter list for display). The signature proves
// origin and denomination only — it is never a spending credential.
// Delegates to nipcash.VerifyProvenance — see Encode's own doc comment for
// why; equivalence across two different secp256k1 library stacks (this
// package used btcsuite's, nipcash uses decred/flokiorg-fork's) was verified
// in lokicash/nmilat_equivalence_test.go before this delegation was adopted.
func VerifyMint(t Token) (minterPubkeyHex string, ok bool) {
	return nipcash.VerifyProvenance(toNipcashToken(t))
}

// writeTLV is kept for this package's own test suite, which builds raw,
// deliberately malformed TLV byte streams to exercise Decode's rejection
// paths directly - not used by Encode/Decode themselves anymore, which
// delegate to nipcash.
func writeTLV(buf *bytes.Buffer, typ uint8, value []byte) {
	buf.WriteByte(typ)
	buf.WriteByte(uint8(len(value))) //nolint:gosec // every caller pre-validates len(value) fits a byte before calling writeTLV
	buf.Write(value)
}
