import useSWR from "swr";

import { LokiInfo } from "src/types";
import { swrFetcher } from "src/utils/swr";

export function useLokiInfo() {
  return useSWR<LokiInfo>("/api/loki/info", swrFetcher, {
    dedupingInterval: 5 * 60 * 1000, // 5 minutes
  });
}
