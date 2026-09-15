import { nip19 } from "nostr-tools";

// safeNpubEncode never throws on a malformed hex pubkey — callers always have
// a safe fallback (e.g. rendering initials) instead of crashing.
export function safeNpubEncode(hex: string): string | undefined {
  try {
    return nip19.npubEncode(hex);
  } catch {
    return undefined;
  }
}

// shortenMiddle truncates a long identifier to its beginning and end (e.g.
// "npub1sg6plzptd6…jsf1a2b3") instead of just its beginning — showing both
// ends lets someone eyeball-match it against another display of the same
// npub, which a prefix-only truncation doesn't support.
export function shortenMiddle(value: string, headLen = 12, tailLen = 6): string {
  if (value.length <= headLen + tailLen) {
    return value;
  }
  return `${value.slice(0, headLen)}…${value.slice(-tailLen)}`;
}

export interface DecodedNostrPointer {
  hex: string;
  // Only nprofile1... carries relay hints (the publisher's own claim of
  // where they can be found) — npub/hex have none.
  relays?: string[];
}

// safeDecodeToPointer accepts a raw hex pubkey, npub1..., or nprofile1... and
// normalizes it to a hex pubkey, preserving an nprofile's embedded relay
// hints so a caller can use the exact relays the pasted identity pointed at
// (e.g. to fetch that profile, or to prefill an Identity Authority's
// relay_urls) instead of only a generic relay pool. Returns undefined for
// anything unparseable instead of throwing.
export function safeDecodeToPointer(input: string): DecodedNostrPointer | undefined {
  const trimmed = input.trim();
  if (/^[0-9a-fA-F]{64}$/.test(trimmed)) {
    return { hex: trimmed.toLowerCase() };
  }
  try {
    const decoded = nip19.decode(trimmed);
    if (decoded.type === "npub") {
      return { hex: decoded.data as string };
    }
    if (decoded.type === "nprofile") {
      const data = decoded.data as { pubkey: string; relays?: string[] };
      return {
        hex: data.pubkey,
        relays: data.relays && data.relays.length > 0 ? data.relays : undefined,
      };
    }
  } catch {
    // fall through
  }
  return undefined;
}

// safeDecodeToHex accepts a raw hex pubkey, npub1..., or nprofile1... and
// normalizes it to a hex pubkey — callers (e.g. identity pubkey inputs) can
// let users paste whichever format they have on hand, but the backend and
// NDK author-filters always expect hex. Returns undefined for anything
// unparseable instead of throwing.
export function safeDecodeToHex(input: string): string | undefined {
  return safeDecodeToPointer(input)?.hex;
}
