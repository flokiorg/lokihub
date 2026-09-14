import { bech32Decode, convertBits } from "src/utils/bech32";

const NCONNECTION_PREFIX = "nconnection";

// TLV type numbers, matching nipIC/nconnection.go exactly
// (github.com/ohstr/nmilat/nipIC) — a decoder must agree with the encoder's
// numbering byte-for-byte, not just the general shape.
const TLV_CONNECTION_KEY = 0;
const TLV_RELAY = 1;
const TLV_PLATFORM = 2;

const utf8Decoder = new TextDecoder("utf-8");

function bytesToHex(bytes: number[]): string {
  return bytes.map((b) => b.toString(16).padStart(2, "0")).join("");
}

export interface DecodedNConnection {
  connectionKey: string; // hex, 32 bytes — NIP-IC's ConnectionKey
  relays: string[];
  platform?: string;
}

// decodeNConnection parses an nconnection1... string (NIP-IC's
// EncodeNConnection) into its ConnectionKey, relay hints, and optional
// platform name. This is the client-side counterpart to an operator having
// to already know/compute a raw ConnectionKey hex by hand — whoever
// generated the connection (a Discord bot, a support tool, whatever speaks
// NIP-IC on the other platform) hands over one shareable string instead,
// the same NIP-19-style convenience nprofile/lokicash1.../etc. already give.
//
// Returns undefined for anything malformed (wrong prefix, bad checksum,
// missing/wrong-length ConnectionKey TLV) rather than throwing, so callers
// can treat this the same as any other "is this a valid paste yet" check —
// see safeDecodeToHex's identical convention for pubkey-shaped input.
export function decodeNConnection(
  input: string
): DecodedNConnection | undefined {
  const trimmed = input.trim();
  const decoded = bech32Decode(trimmed);
  if (!decoded || decoded.prefix !== NCONNECTION_PREFIX) {
    return undefined;
  }
  const data = convertBits(decoded.words, 5, 8, false);
  if (!data) {
    return undefined;
  }

  let connectionKeyBytes: number[] | undefined;
  const relays: string[] = [];
  let platform: string | undefined;

  let pos = 0;
  while (pos + 2 <= data.length) {
    const t = data[pos];
    const l = data[pos + 1];
    pos += 2;
    if (pos + l > data.length) {
      break;
    }
    const v = data.slice(pos, pos + l);
    pos += l;

    if (t === TLV_CONNECTION_KEY) {
      if (l !== 32) {
        return undefined;
      }
      connectionKeyBytes = v;
    } else if (t === TLV_RELAY) {
      relays.push(utf8Decoder.decode(new Uint8Array(v)));
    } else if (t === TLV_PLATFORM) {
      platform = utf8Decoder.decode(new Uint8Array(v));
    }
    // Any other type is ignored, same "unknown TLV type: ignore" rule
    // NIP-19/lokicash1... decoders follow, so a future field doesn't break
    // this decoder.
  }

  if (!connectionKeyBytes) {
    return undefined;
  }
  return {
    connectionKey: bytesToHex(connectionKeyBytes),
    relays,
    platform,
  };
}
