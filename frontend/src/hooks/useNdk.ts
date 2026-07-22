import { useMemo } from "react";

import { getNdk } from "src/lib/ndk";
import { useInfo } from "src/hooks/useInfo";

function splitRelayUrls(value: string | undefined): string[] {
  return value?.split(",").map((r) => r.trim()).filter(Boolean) ?? [];
}

// useNdk returns a shared NDK instance connected to both the General relays
// (generalRelay) and the dedicated NIP-50 search relays (searchRelay) from
// /api/info — independent of the NWC relay list. relayUrls and
// searchRelayUrls are returned separately so callers can scope each query
// (via NDKRelaySet) to the relay set it actually belongs to, rather than
// querying every configured relay indiscriminately — search in particular
// must stay confined to searchRelayUrls, since generalRelay entries have no
// guarantee of supporting NIP-50 `search` at all (see useNostrProfileSearch).
export function useNdk() {
  const { data: info } = useInfo();
  const relayUrls = useMemo(() => splitRelayUrls(info?.generalRelay), [info]);
  const searchRelayUrls = useMemo(
    () => splitRelayUrls(info?.searchRelay),
    [info]
  );
  const allRelayUrls = useMemo(
    () => Array.from(new Set([...relayUrls, ...searchRelayUrls])),
    [relayUrls, searchRelayUrls]
  );
  const ndk = useMemo(
    () => (allRelayUrls.length ? getNdk(allRelayUrls) : undefined),
    [allRelayUrls]
  );
  return { ndk, relayUrls, searchRelayUrls };
}
