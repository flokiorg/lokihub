import useSWR from "swr";

import type {
  WithdrawEngineStatus,
  WithdrawNodeState,
  WithdrawTermsView,
} from "src/components/withdraw/withdraw";
import { swrFetcher } from "src/utils/swr";

// The on-chain withdrawal admin API (WP-23): the node switch, the payout
// engine's status and a Cash Hub's terms.
export function useWithdrawNode() {
  return useSWR<WithdrawNodeState>("/api/cash-withdraw/node", swrFetcher);
}

export function useWithdrawStatus(enabled: boolean) {
  return useSWR<WithdrawEngineStatus>(
    enabled ? "/api/cash-withdraw/status" : null,
    swrFetcher
  );
}

export function useHubWithdrawTerms(hubId: number | undefined) {
  return useSWR<WithdrawTermsView>(
    hubId ? `/api/cash-hubs/${hubId}/withdraw-terms` : null,
    swrFetcher
  );
}
