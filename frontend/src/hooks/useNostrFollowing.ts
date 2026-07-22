import React from "react";
import useSWR from "swr";

import { useNdk } from "src/hooks/useNdk";
import { getRelaySetForPubkey } from "src/lib/nostrRelaySet";

// useNostrFollowing resolves a pubkey's kind:3 contact list (NIP-02) into a
// flat array of followed pubkeys, client-side — used to preview/pick circle
// members before a hub exists. This mirrors the backend's own kind:3 fetch
// in service/nostr_social_cache.go (same Kinds:[3], Limit:1 approach, since
// kind:3 is replaceable and only the latest event matters), so the frontend
// and backend agree on what "following" means.
export function useNostrFollowing(pubkey?: string) {
  const { ndk, relayUrls } = useNdk();

  const { data, isLoading } = useSWR<string[]>(
    ndk && pubkey ? ["nostr-following", pubkey, relayUrls.join(",")] : null,
    async () => {
      const relaySet = await getRelaySetForPubkey(ndk!, pubkey!, relayUrls);
      const events = await ndk!.fetchEvents(
        {
          kinds: [3],
          authors: [pubkey!],
          limit: 1,
        },
        {},
        relaySet
      );
      let latest: { created_at?: number; tags: string[][] } | undefined;
      for (const event of events) {
        if (!latest || (event.created_at ?? 0) > (latest.created_at ?? 0)) {
          latest = event;
        }
      }
      if (!latest) {
        return [];
      }
      return latest.tags
        .filter((tag) => tag[0] === "p" && !!tag[1])
        .map((tag) => tag[1]);
    },
    { revalidateOnFocus: false, dedupingInterval: 5 * 60 * 1000 }
  );

  return React.useMemo(
    () => ({ followingPubkeys: data ?? [], isLoading }),
    [data, isLoading]
  );
}
