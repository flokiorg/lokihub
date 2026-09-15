import { nip05 } from "nostr-tools";
import React from "react";

import { safeDecodeToPointer } from "src/utils/nostr";

const NIP05_LOOKUP_DEBOUNCE_MS = 500;

// useResolvedNostrIdentity normalizes any of hex / npub1... / nprofile1... /
// a NIP-05 address (name@domain.com) into a hex pubkey. The first three
// decode synchronously (safeDecodeToPointer); a NIP-05 address requires an
// async well-known lookup (nostr-tools' nip05.queryProfile), so it's
// debounced and tracked with its own loading state — callers show a spinner
// while `isResolving` is true instead of flashing an "invalid" state
// mid-lookup. Also surfaces `relayHints` — an nprofile's embedded relays, or
// a NIP-05 .well-known's own "relays" mapping — so a caller can fetch from
// (or memorize) the exact relays the identity pointed at, not just a generic
// pool.
export function useResolvedNostrIdentity(rawInput: string) {
  const trimmed = rawInput.trim();
  const syncPointer = trimmed ? safeDecodeToPointer(trimmed) : undefined;
  const syncHex = syncPointer?.hex;
  const isNip05Candidate = !syncHex && nip05.isNip05(trimmed);

  const [nip05Result, setNip05Result] = React.useState<
    { pubkey: string; relays?: string[] } | undefined
  >(undefined);
  const [isResolving, setResolving] = React.useState(false);

  React.useEffect(() => {
    if (!isNip05Candidate) {
      setNip05Result(undefined);
      setResolving(false);
      return;
    }
    let cancelled = false;
    setResolving(true);
    const timer = setTimeout(async () => {
      try {
        const pointer = await nip05.queryProfile(trimmed);
        if (!cancelled) {
          setNip05Result(pointer ?? undefined);
        }
      } catch {
        if (!cancelled) {
          setNip05Result(undefined);
        }
      } finally {
        if (!cancelled) {
          setResolving(false);
        }
      }
    }, NIP05_LOOKUP_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [trimmed, isNip05Candidate]);

  const hex = syncHex ?? nip05Result?.pubkey;
  const resolving = isNip05Candidate && isResolving;
  const isInvalid = trimmed.length > 0 && !hex && !resolving;
  const relayHints = syncPointer?.relays ?? nip05Result?.relays;

  return { hex, isResolving: resolving, isInvalid, relayHints };
}
