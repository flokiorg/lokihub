import {
  feeRate as feeRateBase,
  loki as lokiBase,
} from "src/components/withdraw/payouts";
import { useHubInfo } from "src/hooks/useHubInfo";

/**
 * Binds the payouts loki()/feeRate() string helpers to the hub's native base
 * unit (nativeAsset.unitName), so callers render e.g. "sat" / "sat/vB" for a
 * non-Flokicoin chain without threading the unit through every call site.
 * Falls back to "loki" while hub info is loading.
 */
export function useNativeAmountFormat() {
  const { data: hubInfo } = useHubInfo();
  const unit = hubInfo?.nativeAsset?.unitName ?? "loki";
  return {
    loki: (millis: number) => lokiBase(millis, unit),
    feeRate: (v: Parameters<typeof feeRateBase>[0]) => feeRateBase(v, unit),
  };
}
