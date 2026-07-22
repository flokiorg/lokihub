import React from "react";
import { nip05 } from "nostr-tools";

import { NostrProfile } from "src/hooks/useNostrProfiles";

const EMPTY_SET: Set<string> = new Set();
const VERIFICATION_CONCURRENCY = 6;

// Session-lived cache of pubkey+nip05 -> verified, so re-opening the member
// picker (or navigating back to it) doesn't repeat lookups that already
// resolved. NIP-05 bindings rarely change, so there's no TTL/invalidation.
const verifiedCache = new Map<string, boolean>();

function cacheKey(pubkey: string, address: string) {
  return `${pubkey}:${address}`;
}

// useNip05Verification cryptographically confirms that a profile's claimed
// nip05 address actually resolves back to its pubkey (fetches
// <domain>/.well-known/nostr.json and compares), rather than trusting the
// nip05 string in the kind:0 event as-is. Pass an empty map while the UI
// that needs this is closed/hidden — callers gate `profiles` on visibility
// so lookups only happen for contacts someone is actually looking at.
// Requests are concurrency-capped rather than fired all at once, and each
// pubkey moves from `pending` to `verified` (or just drops out of `pending`
// on failure) as its own lookup resolves, so results appear incrementally
// instead of all-or-nothing.
export function useNip05Verification(profiles: Map<string, NostrProfile>) {
  const candidates = React.useMemo(
    () =>
      Array.from(profiles.entries()).filter(
        (entry): entry is [string, NostrProfile & { nip05: nip05.Nip05 }] =>
          nip05.isNip05(entry[1].nip05)
      ),
    [profiles]
  );

  const [verified, setVerified] = React.useState<Set<string>>(EMPTY_SET);
  const [pending, setPending] = React.useState<Set<string>>(EMPTY_SET);

  React.useEffect(() => {
    let cancelled = false;

    const alreadyVerified = new Set<string>();
    const toFetch: [string, nip05.Nip05][] = [];
    for (const [pubkey, profile] of candidates) {
      const cached = verifiedCache.get(cacheKey(pubkey, profile.nip05));
      if (cached === true) {
        alreadyVerified.add(pubkey);
      } else if (cached === undefined) {
        toFetch.push([pubkey, profile.nip05]);
      }
    }

    setVerified(alreadyVerified);
    setPending(new Set(toFetch.map(([pubkey]) => pubkey)));

    if (toFetch.length === 0) {
      return;
    }

    let nextIndex = 0;
    const workers = Array.from(
      { length: Math.min(VERIFICATION_CONCURRENCY, toFetch.length) },
      async () => {
        while (!cancelled) {
          const i = nextIndex++;
          if (i >= toFetch.length) {
            return;
          }
          const [pubkey, address] = toFetch[i];
          let ok = false;
          try {
            ok = await nip05.isValid(pubkey, address);
          } catch {
            ok = false;
          }
          verifiedCache.set(cacheKey(pubkey, address), ok);
          if (cancelled) {
            return;
          }
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
        }
      }
    );
    void Promise.all(workers);

    return () => {
      cancelled = true;
    };
  }, [candidates]);

  return { verified, pending };
}
