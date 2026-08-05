// Package lokicash implements the lokicash1... bech32 token that packages a
// JIT Wallet's NWC pairing data (NIP-JW §The Lokicash Token) as one
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
	"math"

	"github.com/btcsuite/btcd/btcutil/bech32"
)

// HRP is the bech32 human-readable prefix lokihub issues its own tokens
// under.
const HRP = "lokicash"

// TLV type numbers within a lokicash-family token. 0 and 1 follow the same
// convention NIP-19 uses for nprofile/nevent/naddr (0 is the token's
// primary identifier, 1 is a relay hint); 2 is specific to this token
// family. 3 and 4 are optional metadata hints, added after the format's
// initial release — decoders written before they existed still parse
// everything else correctly (the "unknown TLV type: ignore" rule below is
// exactly what makes that safe).
const (
	tlvWalletPubkey     uint8 = 0
	tlvRelay            uint8 = 1
	tlvSecret           uint8 = 2
	tlvIdentityRequired uint8 = 3
	tlvMaxTransfers     uint8 = 4
)

// keyLen is the byte length of both a wallet pubkey and a pairing secret —
// raw 32-byte values, same as every other Nostr key.
const keyLen = 32

// maxTLVValueLen is the largest value a single TLV entry's one-byte length
// field can hold. A relay URL longer than this would silently truncate the
// length prefix instead of erroring, corrupting every entry that follows it
// in the stream — Encode rejects it outright instead.
const maxTLVValueLen = 255

// Token is the decoded content of a lokicash-family bech32 string: the
// pieces of NIP-47 pairing data a JIT Wallet connection needs (NIP-JW §The
// Pairing Connection), plus two optional metadata hints (NIP-JW §The
// Lokicash Token → Redemption Metadata).
//
// IdentityRequired and MaxTransfers are both pointers specifically so a
// caller can tell "this token doesn't carry this hint" (nil — true of any
// token minted before these fields existed, or one an encoder chose not to
// populate) apart from a real, meaningful zero value. Treat both as
// best-effort hints only, snapshotted at whatever moment the token was
// minted or last re-derived — NOT a live guarantee: the wallet's actual
// identity requirement or transfer cap can change afterward via
// jit_transfer, same as everything else about a JIT Wallet connection.
// Every jit_redeem/jit_transfer call is still authoritatively checked
// server-side regardless of what a token implies; a client MUST NOT treat
// either field as a substitute for that check, only as a hint for deciding
// how to attempt one.
type Token struct {
	HRP          string   // e.g. "lokicash", "satscash"
	WalletPubkey string   // hex, 32 bytes
	Secret       string   // hex, 32 bytes — the NWC connection secret
	RelayURLs    []string // in encoded order
	// IdentityRequired: true means every slice this wallet currently serves
	// is identity-bound (jit_redeem/jit_transfer need a signed proof); false
	// means the wallet is a single bearer slice (only its secret is needed —
	// no proof, no Nostr identity at all). Always uniform across a wallet's
	// whole recipient set (NIP-JW: a bearer slice is always the wallet's
	// only one), so this is well-defined per wallet, not per slice.
	IdentityRequired *bool
	// MaxTransfers mirrors the wallet's own jit_transfer cap: 0 means
	// unlimited, N means each slice may be transferred at most N times
	// before it can only be redeemed. Also uniform across the wallet's
	// recipient set.
	MaxTransfers *int
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
	if t.MaxTransfers != nil && (*t.MaxTransfers < 0 || uint(*t.MaxTransfers) > math.MaxUint32) {
		return "", fmt.Errorf("lokicash: max_transfers %d does not fit in the token's 4-byte field", *t.MaxTransfers)
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
	if t.MaxTransfers != nil {
		var v [4]byte
		binary.BigEndian.PutUint32(v[:], uint32(*t.MaxTransfers)) //nolint:gosec // range-checked above
		writeTLV(buf, tlvMaxTransfers, v[:])
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
	haveMaxTransfers := false
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
		case tlvMaxTransfers:
			if len(value) != 4 {
				return Token{}, fmt.Errorf("lokicash: max_transfers must be 4 bytes, got %d", len(value))
			}
			if haveMaxTransfers {
				return Token{}, fmt.Errorf("lokicash: duplicate max_transfers entry")
			}
			maxTransfers := int(binary.BigEndian.Uint32(value)) //nolint:gosec // a 4-byte TLV field is always far below int's range on any supported platform
			result.MaxTransfers = &maxTransfers
			haveMaxTransfers = true
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
	return result, nil
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
