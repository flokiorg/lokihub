import useSWR from "swr";

import { CircleIdentityResponse } from "src/types";
import { swrFetcher } from "src/utils/swr";

// useCircleIdentity fetches full detail for a single CircleIdentity,
// including usedByCount — how many circle_hub apps currently share it.
export function useCircleIdentity(id: number | undefined) {
  return useSWR<CircleIdentityResponse>(
    id ? `/api/circle-identities/${id}` : null,
    swrFetcher
  );
}
