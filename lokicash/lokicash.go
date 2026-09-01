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
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
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
// 8-byte big-endian mloki amount.
const attestedAmountLen = 8

// mintPayloadScheme is the fixed, versioned tag prefixing every mint-signature
// payload, regardless of coin/HRP. Bumping the version invalidates old
// signatures deliberately.
const mintPayloadScheme = "lokicash-mint:v1"

// LNSignedMessagePrefix is the context prefix the flnd node prepends to every
// message before double-SHA256-hashing and signing it (flnd rpcserver.go's
// signedMsgPrefix). A mint signature is produced by the node's own
// SignMessage, so VerifyMint MUST reproduce this exact prefixing to recover the
// signer — the same string flnd's own VerifyMessage uses.
const LNSignedMessagePrefix = "Flokicoin Lightning Signed Message:"

// maxTLVValueLen is the largest value a single TLV entry's one-byte length
// field can hold. A relay URL longer than this would silently truncate the
// length prefix instead of erroring, corrupting every entry that follows it
// in the stream — Encode rejects it outright instead.
const maxTLVValueLen = 255

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
// WalletPubkey and Secret MUST each be a 32-byte hex string.
func Encode(t Token) (string, error) {
	pubkey, err := decodeKeyHex(t.WalletPubkey, "wallet pubkey")
	if err != nil {
		return "", err
	}
	secret, err := decodeKeyHex(t.Secret, "secret")
	if err != nil {
		return "", err
	}
	for _, url := range t.RelayURLs {
		if len(url) > maxTLVValueLen {
			return "", fmt.Errorf("lokicash: relay url exceeds %d bytes: %q", maxTLVValueLen, url)
		}
	}

	// Mint provenance is both-or-neither: an encoder must never emit a lone
	// signature or a lone amount (§Mint Provenance). Reject the half-pair here
	// rather than silently producing a token a decoder would strip.
	if (t.MintSignature == nil) != (t.AttestedAmount == nil) {
		return "", fmt.Errorf("lokicash: mint signature and attested amount must be set together")
	}
	if t.MintSignature != nil && len(t.MintSignature) != mintSigLen {
		return "", fmt.Errorf("lokicash: mint signature must be %d bytes, got %d", mintSigLen, len(t.MintSignature))
	}

	buf := &bytes.Buffer{}
	writeTLV(buf, tlvWalletPubkey, pubkey)
	for _, url := range t.RelayURLs {
		writeTLV(buf, tlvRelay, []byte(url))
	}
	writeTLV(buf, tlvSecret, secret)
	if t.IdentityRequired != nil {
		var b byte
		if *t.IdentityRequired {
			b = 1
		}
		writeTLV(buf, tlvIdentityRequired, []byte{b})
	}
	if t.MintSignature != nil {
		var amountBytes [attestedAmountLen]byte
		binary.BigEndian.PutUint64(amountBytes[:], *t.AttestedAmount)
		writeTLV(buf, tlvMintSignature, t.MintSignature)
		writeTLV(buf, tlvAttestedAmount, amountBytes[:])
	}

	bits5, err := bech32.ConvertBits(buf.Bytes(), 8, 5, true)
	if err != nil {
		return "", fmt.Errorf("lokicash: failed to convert bits: %w", err)
	}
	return bech32.Encode(t.HRP, bits5)
}

// Decode parses a lokicash-family bech32 token back into its pairing data.
// It rejects anything missing either required field (wallet pubkey or
// secret), carrying a malformed length for either, or repeating either one —
// a caller that skipped this check could otherwise hand a recipient a
// connection string for the wrong wallet, or one with a truncated or
// ambiguous secret that fails to pair, or pairs with the wrong party.
func Decode(token string) (Token, error) {
	hrp, bits5, err := bech32.DecodeNoLimit(token)
	if err != nil {
		return Token{}, fmt.Errorf("lokicash: invalid bech32: %w", err)
	}
	data, err := bech32.ConvertBits(bits5, 5, 8, false)
	if err != nil {
		return Token{}, fmt.Errorf("lokicash: failed to convert bits: %w", err)
	}

	result := Token{HRP: hrp}
	haveWalletPubkey := false
	haveSecret := false
	haveIdentityRequired := false
	// Mint provenance (types 5/6) is optional and both-or-neither. Anything
	// anomalous about it — a lone half, a duplicate, a wrong-length value —
	// MUST leave the token with no provenance rather than fail the whole
	// decode (§Mint Provenance). We collect the pair tentatively and poison it
	// on any anomaly, then attach it only if a clean pair survived.
	var mintSig []byte
	var attestedAmount *uint64
	provenancePoisoned := false
	curr := 0
	for curr < len(data) {
		typ, value, ok := readTLV(data[curr:])
		if !ok {
			return Token{}, fmt.Errorf("lokicash: truncated TLV entry at offset %d", curr)
		}
		switch typ {
		case tlvWalletPubkey:
			if len(value) != keyLen {
				return Token{}, fmt.Errorf("lokicash: wallet pubkey must be %d bytes, got %d", keyLen, len(value))
			}
			if haveWalletPubkey {
				return Token{}, fmt.Errorf("lokicash: duplicate wallet pubkey entry")
			}
			result.WalletPubkey = hex.EncodeToString(value)
			haveWalletPubkey = true
		case tlvRelay:
			result.RelayURLs = append(result.RelayURLs, string(value))
		case tlvSecret:
			if len(value) != keyLen {
				return Token{}, fmt.Errorf("lokicash: secret must be %d bytes, got %d", keyLen, len(value))
			}
			if haveSecret {
				return Token{}, fmt.Errorf("lokicash: duplicate secret entry")
			}
			result.Secret = hex.EncodeToString(value)
			haveSecret = true
		case tlvIdentityRequired:
			if len(value) != 1 {
				return Token{}, fmt.Errorf("lokicash: identity_required must be 1 byte, got %d", len(value))
			}
			if value[0] > 1 {
				return Token{}, fmt.Errorf("lokicash: identity_required must be 0 or 1, got %d", value[0])
			}
			if haveIdentityRequired {
				return Token{}, fmt.Errorf("lokicash: duplicate identity_required entry")
			}
			identityRequired := value[0] == 1
			result.IdentityRequired = &identityRequired
			haveIdentityRequired = true
		case tlvMintSignature:
			// Poison rather than error: provenance is optional metadata whose
			// malformation MUST NOT brick an otherwise-valid connection token.
			if mintSig != nil || len(value) != mintSigLen {
				provenancePoisoned = true
				break
			}
			mintSig = append([]byte(nil), value...)
		case tlvAttestedAmount:
			if attestedAmount != nil || len(value) != attestedAmountLen {
				provenancePoisoned = true
				break
			}
			amt := binary.BigEndian.Uint64(value)
			attestedAmount = &amt
		default:
			// Unknown TLV type: ignore, same as NIP-19's own decoders, so a
			// future field can be added without breaking older decoders.
		}
		curr += 2 + len(value)
	}

	if !haveWalletPubkey {
		return Token{}, fmt.Errorf("lokicash: missing wallet pubkey")
	}
	if !haveSecret {
		return Token{}, fmt.Errorf("lokicash: missing secret")
	}
	// Attach provenance only if a clean, complete pair survived. A lone half or
	// any poisoned entry leaves the token with no provenance — never an error.
	if !provenancePoisoned && mintSig != nil && attestedAmount != nil {
		result.MintSignature = mintSig
		result.AttestedAmount = attestedAmount
	}
	return result, nil
}

// MintPayload returns the canonical ASCII string a mint signature commits to
// (NIP-CASH §Mint Provenance): "lokicash-mint:v1:<hrp>:<wallet_pubkey_hex>:<amount_mloki>".
// The minter signs this at mint time and a verifier recomputes it from a
// token's own fields — so both the signer (cashwallet) and VerifyMint MUST use
// this one function, never hand-build the string, to stay byte-identical.
func MintPayload(hrp, walletPubkeyHex string, amountMloki uint64) string {
	return mintPayloadScheme + ":" + hrp + ":" + walletPubkeyHex + ":" + strconv.FormatUint(amountMloki, 10)
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
func VerifyMint(t Token) (minterPubkeyHex string, ok bool) {
	if t.MintSignature == nil || t.AttestedAmount == nil {
		return "", false
	}
	if len(t.MintSignature) != mintSigLen {
		return "", false
	}
	payload := MintPayload(t.HRP, t.WalletPubkey, *t.AttestedAmount)
	// The node's SignMessage prepends LNSignedMessagePrefix and signs a compact
	// recoverable signature over the double-SHA256 of the prefixed message;
	// recovery MUST reproduce that exact digest to reconstruct the signer.
	digest := chainhash.DoubleHashB([]byte(LNSignedMessagePrefix + payload))
	pub, _, err := ecdsa.RecoverCompact(t.MintSignature, digest)
	if err != nil {
		return "", false
	}
	return hex.EncodeToString(pub.SerializeCompressed()), true
}

func decodeKeyHex(s, field string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("lokicash: invalid %s hex: %w", field, err)
	}
	if len(b) != keyLen {
		return nil, fmt.Errorf("lokicash: %s must be %d bytes, got %d", field, keyLen, len(b))
	}
	return b, nil
}

func writeTLV(buf *bytes.Buffer, typ uint8, value []byte) {
	buf.WriteByte(typ)
	buf.WriteByte(uint8(len(value))) //nolint:gosec // every Encode call site pre-validates len(value) <= maxTLVValueLen before calling writeTLV
	buf.Write(value)
}

func readTLV(data []byte) (typ uint8, value []byte, ok bool) {
	if len(data) < 2 {
		return 0, nil, false
	}
	typ = data[0]
	length := int(data[1])
	if len(data) < 2+length {
		return 0, nil, false
	}
	return typ, data[2 : 2+length], true
}
