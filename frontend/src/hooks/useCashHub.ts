import useSWR from "swr";

import {
  CashHubStats,
  CashSliceStatus,
  ListCashWalletClaimsResponse,
} from "src/types";
import { swrFetcher } from "src/utils/swr";

// Cash data refreshes on a timer because a bill's state changes without the
// operator doing anything — a recipient redeems, a window expires, the sweep
// reclaims. 10s rather than the 3s the old hand-rolled polling used: that
// interval predates SWR being used here at all, and three times a second was
// more about the list having no other way to update than about how fast cash
// actually moves.
const CASH_REFRESH_INTERVAL_MS = 10_000;

export function useCashHubStats(hubId: number | undefined, poll = true) {
  return useSWR<CashHubStats>(
    !!hubId && `/api/apps/${hubId}/cash-stats`,
    swrFetcher,
    { refreshInterval: poll ? CASH_REFRESH_INTERVAL_MS : 0 }
  );
}

export type CashClaimsQuery = {
  hubId: number | undefined;
  limit: number;
  offset: number;
  // "" is unfiltered. The legacy "claimed" umbrella still works server-side.
  status: CashSliceStatus | "" | "claimed";
};

export function useCashClaims({
  hubId,
  limit,
  offset,
  status,
}: CashClaimsQuery) {
  const query = new URLSearchParams({
    limit: String(limit),
    offset: String(offset),
  });
  if (status) {
    query.set("status", status);
  }
  return useSWR<ListCashWalletClaimsResponse>(
    !!hubId && `/api/apps/${hubId}/cash-wallets?${query.toString()}`,
    swrFetcher,
    {
      refreshInterval: CASH_REFRESH_INTERVAL_MS,
      // Without this the list blanks on every facet or page change, which is
      // what the old implementation did by replacing the rows with the word
      // "Loading…".
      keepPreviousData: true,
    }
  );
}
