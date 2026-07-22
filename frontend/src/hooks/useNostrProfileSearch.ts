import { NDKRelaySet, profileFromEvent } from "@nostr-dev-kit/ndk";
import React from "react";

import { useNdk } from "src/hooks/useNdk";
import { NostrProfile } from "src/hooks/useNostrProfiles";

const SEARCH_DEBOUNCE_MS = 400;
const MIN_QUERY_LENGTH = 2;
const SEARCH_LIMIT = 8;
// ndk.fetchEvents only resolves once the relay sends EOSE — it has no
// built-in timeout. A relay that doesn't understand NIP-50's `search`
// field may never send one for this subscription (rather than eosing
// immediately with zero matches), which would otherwise hang "Searching…"
// forever instead of falling back to "no matches".
const SEARCH_TIMEOUT_MS = 6000;

export interface NostrProfileSearchResult {
  pubkey: string;
  profile: NostrProfile;
}

// useNostrProfileSearch runs a NIP-50 full-text profile search (kind:0 with
// a `search` filter) against the dedicated searchRelay list from Settings —
// deliberately NOT the generalRelay set every other Circles lookup uses,
// since there's no guarantee those relays implement NIP-50 at all. Mixing in
// non-search relays previously meant NDK would wait on subscription-level
// EOSE from all/most of them before resolving (see eoseReceived in
// @nostr-dev-kit/ndk), so a relay that silently ignores `search` and never
// EOSEs could block real, already-received matches from ever surfacing.
// Whether this returns anything depends on at least one configured search
// relay implementing NIP-50; an unsupporting relay set just yields no
// matches rather than failing loudly, since callers already have a manual
// paste-npub fallback for that case.
export function useNostrProfileSearch(query: string) {
  const { ndk, searchRelayUrls } = useNdk();
  const trimmed = query.trim();
  const [results, setResults] = React.useState<NostrProfileSearchResult[]>([]);
  const [isSearching, setSearching] = React.useState(false);

  React.useEffect(() => {
    if (!ndk || searchRelayUrls.length === 0 || trimmed.length < MIN_QUERY_LENGTH) {
      setResults([]);
      setSearching(false);
      return;
    }
    let cancelled = false;
    setSearching(true);
    const timer = setTimeout(async () => {
      try {
        const relaySet = NDKRelaySet.fromRelayUrls(searchRelayUrls, ndk);
        const events = await Promise.race([
          ndk.fetchEvents(
            { kinds: [0], search: trimmed, limit: SEARCH_LIMIT },
            {},
            relaySet
          ),
          new Promise<Set<never>>((resolve) =>
            setTimeout(() => resolve(new Set()), SEARCH_TIMEOUT_MS)
          ),
        ]);
        if (cancelled) {
          return;
        }
        setResults(
          Array.from(events).map((event) => {
            const raw = profileFromEvent(event);
            const profile: NostrProfile = {
              name: raw.name,
              displayName: raw.displayName,
              picture: raw.picture ?? raw.image,
              about: raw.about ?? raw.bio,
              nip05: raw.nip05,
              lud16: raw.lud16,
            };
            return { pubkey: event.pubkey, profile };
          })
        );
      } catch {
        if (!cancelled) {
          setResults([]);
        }
      } finally {
        if (!cancelled) {
          setSearching(false);
        }
      }
    }, SEARCH_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [ndk, searchRelayUrls, trimmed]);

  return { results, isSearching };
}
