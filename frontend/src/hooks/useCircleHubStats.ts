import useSWR from "swr";

import { CircleHubStats } from "src/types";
import { swrFetcher } from "src/utils/swr";

// Circle stats refresh on a timer for the same reason cash stats do: a
// circle's numbers change without the host doing anything — a member spends,
// a budget renews. These are rolled-up totals rather than rows the host is
// acting on, so they tick at the same 10s as the cash dashboards.
const CIRCLE_REFRESH_INTERVAL_MS = 10_000;

export function useCircleHubStats(appId: number | undefined, poll = true) {
  return useSWR<CircleHubStats>(
    !!appId && `/api/apps/${appId}/circle-stats`,
    swrFetcher,
    { refreshInterval: poll ? CIRCLE_REFRESH_INTERVAL_MS : 0 }
  );
}
