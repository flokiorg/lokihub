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
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcutil/bech32"
)

// HRP is the bech32 human-readable prefix lokihub issues its own tokens
// under.
const HRP = "lokicash"

// TLV type numbers within a lokicash-family token. 0 and 1 follow the same
// convention NIP-19 uses for nprofile/nevent/naddr (0 is the token's
// primary identifier, 1 is a relay hint); 2 is specific to this token
// family.
const (
	tlvWalletPubkey uint8 = 0
	tlvRelay        uint8 = 1
	tlvSecret       uint8 = 2
)

// keyLen is the byte length of both a wallet pubkey and a pairing secret —
// raw 32-byte values, same as every other Nostr key.
const keyLen = 32

// maxTLVValueLen is the largest value a single TLV entry's one-byte length
// field can hold. A relay URL longer than this would silently truncate the
// length prefix instead of erroring, corrupting every entry that follows it
// in the stream — Encode rejects it outright instead.
const maxTLVValueLen = 255

// Token is the decoded content of a lokicash-family bech32 string: exactly
// the pieces of NIP-47 pairing data a JIT Wallet connection needs (NIP-JW
// §The Pairing Connection), nothing else.
type Token struct {
	HRP          string   // e.g. "lokicash", "satscash"
	WalletPubkey string   // hex, 32 bytes
	Secret       string   // hex, 32 bytes — the NWC connection secret
	RelayURLs    []string // in encoded order
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

	buf := &bytes.Buffer{}
	writeTLV(buf, tlvWalletPubkey, pubkey)
	for _, url := range t.RelayURLs {
		writeTLV(buf, tlvRelay, []byte(url))
	}
	writeTLV(buf, tlvSecret, secret)

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
	buf.WriteByte(uint8(len(value)))
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
