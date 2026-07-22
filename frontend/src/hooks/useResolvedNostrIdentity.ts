import { nip05 } from "nostr-tools";
import React from "react";

import { safeDecodeToHex } from "src/utils/nostr";

const NIP05_LOOKUP_DEBOUNCE_MS = 500;

// useResolvedNostrIdentity normalizes any of hex / npub1... / nprofile1... /
// a NIP-05 address (name@domain.com) into a hex pubkey. The first three
// decode synchronously (safeDecodeToHex); a NIP-05 address requires an async
// well-known lookup (nostr-tools' nip05.queryProfile), so it's debounced and
// tracked with its own loading state — callers show a spinner while
// `isResolving` is true instead of flashing an "invalid" state mid-lookup.
export function useResolvedNostrIdentity(rawInput: string) {
  const trimmed = rawInput.trim();
  const syncHex = trimmed ? safeDecodeToHex(trimmed) : undefined;
  const isNip05Candidate = !syncHex && nip05.isNip05(trimmed);

  const [nip05Hex, setNip05Hex] = React.useState<string | undefined>(
    undefined
  );
  const [isResolving, setResolving] = React.useState(false);

  React.useEffect(() => {
    if (!isNip05Candidate) {
      setNip05Hex(undefined);
      setResolving(false);
      return;
    }
    let cancelled = false;
    setResolving(true);
    const timer = setTimeout(async () => {
      try {
        const pointer = await nip05.queryProfile(trimmed);
        if (!cancelled) {
          setNip05Hex(pointer?.pubkey);
        }
      } catch {
        if (!cancelled) {
          setNip05Hex(undefined);
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

  const hex = syncHex ?? nip05Hex;
  const resolving = isNip05Candidate && isResolving;
  const isInvalid = trimmed.length > 0 && !hex && !resolving;

  return { hex, isResolving: resolving, isInvalid };
}
