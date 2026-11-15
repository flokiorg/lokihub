import React from "react";
import { nip05 } from "nostr-tools";

import { NostrProfile } from "src/hooks/useNostrProfiles";

const EMPTY_SET: Set<string> = new Set();

// Session-lived cache of pubkey+nip05 -> verified. A NIP-05 lookup fetches
// <domain>/.well-known/nostr.json directly from the browser, which leaks the
// viewer's IP and which pubkey is being checked to an arbitrary domain — so it
// only ever runs when the user explicitly asks for it (the `verify` action
// below), never automatically. Once the user has consented to a lookup, its
// result is cached for the session (NIP-05 bindings rarely change) so a second
// verify — or re-opening the screen after one — doesn't repeat the request.
const verifiedCache = new Map<string, boolean>();

function cacheKey(pubkey: string, address: string) {
  return `${pubkey}:${address}`;
}

// useNip05Verification tracks, for the currently-visible profiles that carry a
// nip05 claim, which ones have been confirmed to resolve back to their pubkey.
//
// It performs NO network request on its own: a profile's nip05 is treated as an
// unverified claim until the user invokes `verify(pubkey)` — a per-profile,
// user-initiated affordance in the UI — which runs the single on-demand
// nip05.isValid GET for that one profile (now a consented request). `verified`
// is still seeded from the session cache so an already-consented confirmation
// survives re-renders and screen re-opens without re-fetching; seeding is a
// pure read of the cache and never triggers a lookup.
export function useNip05Verification(profiles: Map<string, NostrProfile>) {
  const addresses = React.useMemo(() => {
    const map = new Map<string, nip05.Nip05>();
    for (const [pubkey, profile] of profiles) {
      if (nip05.isNip05(profile.nip05)) {
        map.set(pubkey, profile.nip05);
      }
    }
    return map;
  }, [profiles]);

  const [verified, setVerified] = React.useState<Set<string>>(EMPTY_SET);
  const [pending, setPending] = React.useState<Set<string>>(EMPTY_SET);

  // Seed `verified` from the session cache only — purely a read of
  // already-consented results, never a fetch.
  React.useEffect(() => {
    const seeded = new Set<string>();
    for (const [pubkey, address] of addresses) {
      if (verifiedCache.get(cacheKey(pubkey, address)) === true) {
        seeded.add(pubkey);
      }
    }
    setVerified(seeded.size === 0 ? EMPTY_SET : seeded);
    setPending(EMPTY_SET);
  }, [addresses]);

  const verify = React.useCallback(
    async (pubkey: string) => {
      const address = addresses.get(pubkey);
      if (!address) {
        return;
      }
      const cached = verifiedCache.get(cacheKey(pubkey, address));
      if (cached !== undefined) {
        if (cached) {
          setVerified((prev) =>
            prev.has(pubkey) ? prev : new Set(prev).add(pubkey)
          );
        }
        return;
      }
      setPending((prev) =>
        prev.has(pubkey) ? prev : new Set(prev).add(pubkey)
      );
      let ok = false;
      try {
        // User-initiated: the single, now-consented GET of
        // <domain>/.well-known/nostr.json for this one profile.
        ok = await nip05.isValid(pubkey, address);
      } catch {
        ok = false;
      }
      verifiedCache.set(cacheKey(pubkey, address), ok);
      setPending((prev) => {
        if (!prev.has(pubkey)) {
          return prev;
        }
        const next = new Set(prev);
        next.delete(pubkey);
        return next;
      });
      if (ok) {
        setVerified((prev) => new Set(prev).add(pubkey));
      }
    },
    [addresses]
  );

  return { verified, pending, verify };
}
