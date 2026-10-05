import React from "react";
import useSWRInfinite from "swr/infinite";

import { useNdk } from "src/hooks/useNdk";
import { getRelaySetForPubkey } from "src/lib/nostrRelaySet";

export interface NostrNote {
  id: string;
  content: string;
  createdAt: number;
}

type NoteKey = [
  tag: string,
  pubkey: string,
  relayUrls: string,
  until: number | undefined,
];

// useNostrNotes fetches one pubkey's kind:1 notes, newest first, paginated
// via a `until` cursor (the oldest note already fetched) rather than
// infinite scroll — a Load More button matches this app's existing
// plain-button conventions better, and avoids an IntersectionObserver this
// codebase doesn't otherwise use.
//
// useSWRInfinite (not a hand-rolled array of useSWR calls) on purpose: the
// number of pages loaded grows across renders, and calling a hook once per
// page would violate the Rules of Hooks (a changing hook-call count per
// render), which useSWRInfinite's own internal single-hook design avoids.
export function useNostrNotes(pubkey?: string, pageSize = 20) {
  const { ndk, relayUrls } = useNdk();

  const getKey = React.useCallback(
    (_pageIndex: number, previousPageData: NostrNote[] | null) => {
      if (!ndk || !pubkey) {
        return null;
      }
      // A short previous page means there's nothing older left to fetch.
      if (previousPageData && previousPageData.length < pageSize) {
        return null;
      }
      const until = previousPageData?.[previousPageData.length - 1]?.createdAt;
      const key: NoteKey = ["nostr-notes", pubkey, relayUrls.join(","), until];
      return key;
    },
    [ndk, pubkey, relayUrls, pageSize]
  );

  const { data, isValidating, size, setSize } = useSWRInfinite<NostrNote[]>(
    getKey,
    async (key: NoteKey) => {
      const [, authorPubkey, , until] = key;
      const relaySet = await getRelaySetForPubkey(
        ndk!,
        authorPubkey,
        relayUrls
      );
      const events = await ndk!.fetchEvents(
        {
          kinds: [1],
          authors: [authorPubkey],
          limit: pageSize,
          ...(until ? { until } : {}),
        },
        {},
        relaySet
      );
      return Array.from(events)
        .map((event) => ({
          id: event.id,
          content: event.content,
          createdAt: event.created_at ?? 0,
        }))
        .sort((a, b) => b.createdAt - a.createdAt);
    },
    { revalidateOnFocus: false, dedupingInterval: 5 * 60 * 1000 }
  );

  const notes = React.useMemo(() => {
    const seen = new Set<string>();
    const merged: NostrNote[] = [];
    for (const page of data ?? []) {
      for (const note of page) {
        if (!seen.has(note.id)) {
          seen.add(note.id);
          merged.push(note);
        }
      }
    }
    return merged.sort((a, b) => b.createdAt - a.createdAt);
  }, [data]);

  const lastPage = data?.[data.length - 1];
  const hasMore = (lastPage?.length ?? pageSize) >= pageSize;

  const loadMore = React.useCallback(() => {
    if (hasMore) {
      void setSize(size + 1);
    }
  }, [hasMore, setSize, size]);

  return {
    notes,
    isLoading: data === undefined,
    isLoadingMore: isValidating && data !== undefined,
    hasMore,
    loadMore,
  };
}
