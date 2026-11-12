import { useHubInfo } from "src/hooks/useHubInfo";
import { formatLoki } from "src/screens/lsp/types";

/**
 * Binds formatLoki to the hub's native base unit (nativeAsset.unitName) so the
 * lspd dashboard renders e.g. "1,000 sat" for a non-Flokicoin chain without
 * threading the unit through every call site. Falls back to "loki" while hub
 * info is loading, keeping the embedded Flokicoin output byte-for-byte.
 */
export function useFormatLoki(): (value: number | undefined | null) => string {
  const { data: hubInfo } = useHubInfo();
  const unit = hubInfo?.nativeAsset?.unitName ?? "loki";
  return (value) => formatLoki(value, unit);
}
