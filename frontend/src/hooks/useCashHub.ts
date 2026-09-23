import useSWR from "swr";

import { CashHubStats } from "src/types";
import { swrFetcher } from "src/utils/swr";

// Cash stats refresh on a timer because a bill's state changes without the
// operator doing anything — a recipient redeems, a window expires, the sweep
// reclaims. These are rolled-up totals and charts rather than rows the user
// is acting on, so they tick at 10s; CashHubAllocations polls its own list
// faster (3s) because that is where an operator is actually waiting to see a
// redemption land.
const CASH_REFRESH_INTERVAL_MS = 10_000;

export function useCashHubStats(hubId: number | undefined, poll = true) {
  return useSWR<CashHubStats>(
    !!hubId && `/api/apps/${hubId}/cash-stats`,
    swrFetcher,
    { refreshInterval: poll ? CASH_REFRESH_INTERVAL_MS : 0 }
  );
}
