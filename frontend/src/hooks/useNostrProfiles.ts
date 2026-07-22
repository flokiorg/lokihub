import React from "react";
import { profileFromEvent } from "@nostr-dev-kit/ndk";
import { useSWRConfig } from "swr";

import { useNdk } from "src/hooks/useNdk";
import { getRelaySetForPubkeys } from "src/lib/nostrRelaySet";

export interface NostrProfile {
  name?: string;
  displayName?: string;
  picture?: string;
  about?: string;
  nip05?: string;
  lud16?: string;
}

const KIND_METADATA = 0;

export function nostrProfileCacheKey(pubkey: string, relayUrls: string[]) {
  return ["nostr-profile", pubkey, relayUrls.join(",")];
}

// useNostrProfiles fetches kind:0 profile metadata for MANY pubkeys in a
// single relay subscription, instead of one subscription per pubkey — this is
// the key CPU/network optimization for the allowlist "coins" avatar row,
// which would otherwise open up to 8+ concurrent relay subscriptions per
// Circles card. Resolved profiles are also prefilled into the single-pubkey
// useNostrProfile SWR cache, so a later individual lookup of the same pubkey
// (e.g. the same person shown as another circle's own provider) is instant.
//
// Results accumulate in local state keyed by pubkey rather than being fetched
// fresh under one SWR key derived from the whole `pubkeys` array — keying on
// the full array means adding or removing a single pubkey (e.g. pinning one
// more member) produces a brand-new cache key, which SWR treats as a total
// cache miss and refetches every profile in the new set, not just the delta.
// That was visible as every avatar/name in a chip list flickering and
// re-loading whenever just one member was added or removed. Only pubkeys not
// already resolved are ever fetched, and previously-resolved profiles are
// never cleared when the input list changes.
export function useNostrProfiles(pubkeys: string[]) {
  const { ndk, relayUrls } = useNdk();
  const { mutate } = useSWRConfig();
  const [profiles, setProfiles] = React.useState<Map<string, NostrProfile>>(
    new Map()
  );
  const [isLoading, setLoading] = React.useState(false);
  const profilesRef = React.useRef(profiles);
  profilesRef.current = profiles;

  const missingKey = pubkeys
    .filter((pubkey) => !profilesRef.current.has(pubkey))
    .sort()
    .join(",");

  React.useEffect(() => {
    const missing = missingKey ? missingKey.split(",") : [];
    if (!ndk || missing.length === 0) {
      return;
    }
    let cancelled = false;
    setLoading(true);
    (async () => {
      try {
        const relaySet = await getRelaySetForPubkeys(ndk, missing, relayUrls);
        const events = await ndk.fetchEvents(
          { kinds: [KIND_METADATA], authors: missing },
          {},
          relaySet
        );
        if (cancelled) {
          return;
        }
        setProfiles((current) => {
          const next = new Map(current);
          for (const event of events) {
            const raw = profileFromEvent(event);
            const profile: NostrProfile = {
              name: raw.name,
              displayName: raw.displayName,
              picture: raw.picture ?? raw.image,
              about: raw.about ?? raw.bio,
              nip05: raw.nip05,
              lud16: raw.lud16,
            };
            next.set(event.pubkey, profile);
            mutate(nostrProfileCacheKey(event.pubkey, relayUrls), profile, false);
          }
          return next;
        });
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [ndk, relayUrls, missingKey, mutate]);

  return React.useMemo(() => ({ profiles, isLoading }), [profiles, isLoading]);
}
