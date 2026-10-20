import useSWR from "swr";

import { FollowSetResponse } from "src/types";
import { swrFetcher } from "src/utils/swr";

// useCircleFollowSet reads a follow_set identity's set as the hub sees it,
// hidden members included.
export function useCircleFollowSet(identityId: number | undefined) {
  return useSWR<FollowSetResponse>(
    identityId ? `/api/circle-identities/${identityId}/follow-set` : null,
    swrFetcher
  );
}
