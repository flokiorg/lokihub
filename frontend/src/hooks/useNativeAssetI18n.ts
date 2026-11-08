import { useEffect } from "react";

import { useHubInfo } from "src/hooks/useHubInfo";
import i18n from "src/i18n";
import { FLOKICOIN_NATIVE_ASSET } from "src/utils/flokicoinFormatting";

/**
 * Keeps i18next's global interpolation defaults ({{unit}}/{{ticker}}/{{subunit}})
 * in sync with the hub's native asset, so locale strings render the right units
 * without threading values through every t() call. Falls back to the embedded
 * Flokicoin asset until hub info loads.
 */
export function useNativeAssetI18n() {
  const { data: hubInfo } = useHubInfo();
  const asset = hubInfo?.nativeAsset;

  useEffect(() => {
    const a = asset ?? FLOKICOIN_NATIVE_ASSET;
    const vars = { unit: a.unitName, ticker: a.ticker, subunit: a.subunitName };

    if (i18n.options.interpolation) {
      i18n.options.interpolation.defaultVariables = {
        ...i18n.options.interpolation.defaultVariables,
        ...vars,
      };
    }
    // The interpolator caches defaultVariables from init, so update its copy too.
    const interpolator = (
      i18n.services as unknown as {
        interpolator?: { defaultVariables?: Record<string, string> };
      }
    ).interpolator;
    if (interpolator) {
      interpolator.defaultVariables = {
        ...interpolator.defaultVariables,
        ...vars,
      };
    }
    // Re-render already-mounted useTranslation consumers with the new tokens.
    (
      i18n as unknown as { emit: (event: string, ...args: unknown[]) => void }
    ).emit("languageChanged", i18n.language);
  }, [asset]);
}
