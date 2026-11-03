import useSWR from "swr";

import { HubInfo } from "src/types";
import { swrFetcher } from "src/utils/swr";

export function useHubInfo() {
  return useSWR<HubInfo>("/api/hub/info", swrFetcher, {
    dedupingInterval: 5 * 60 * 1000, // 5 minutes
  });
}
