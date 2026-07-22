import useSWR from "swr";

import { CircleIdentitySummary } from "src/types";
import { swrFetcher } from "src/utils/swr";

// useCircleIdentities lists every CircleIdentity, for the "use existing
// identity" picker shown when creating a new circle.
export function useCircleIdentities() {
  return useSWR<{ identities: CircleIdentitySummary[] }>(
    "/api/circle-identities",
    swrFetcher
  );
}
