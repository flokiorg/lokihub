import useSWR from "swr";

import { swrFetcher } from "src/utils/swr";

// useCircleAllowlist lists the allowlisted pubkeys for a circle_hub app
// (only meaningful for allowlist-policy circles — pass enabled=false otherwise).
export function useCircleAllowlist(appId: number, enabled: boolean) {
  return useSWR<{ pubkeys: string[] }>(
    enabled ? `/api/apps/${appId}/circle/allowlist` : null,
    swrFetcher
  );
}
