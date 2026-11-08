import {
  FLOKICOIN_DISPLAY_FORMAT_AUTO,
  FLOKICOIN_DISPLAY_FORMAT_FLC,
} from "src/constants";
import { FlokicoinDisplayFormat } from "src/types";

// 100,000 loki (0.001 FLC) threshold for AUTO mode
const AUTO_UNIT_THRESHOLD_LOKI = 100_000;

export interface NativeAssetExplorer {
  label: string;
  url: string;
  txPath: string;
}

// NativeAssetFormat is the on-chain asset descriptor exactly as the backend
// returns it on GET /api/hub/info (hubinfo.HubInfo.nativeAsset / seed.NativeAsset)
// — same field names and units — so formatNativeAmount can consume
// useHubInfo().nativeAsset directly. The embedded Flokicoin default is
// FLOKICOIN_NATIVE_ASSET.
export interface NativeAssetFormat {
  ticker: string; // unit of account shown to users, e.g. "FLC"
  symbol: string; // symbol/glyph, often equal to ticker
  unitName: string; // base on-chain unit, e.g. "loki"
  subunitName: string; // accounting base (msat-equivalent), e.g. "mloki"
  decimals: number; // units per ticker = 10^decimals (1 FLC = 1e8 loki)
  subunitPerUnit: number; // subunits per unit, e.g. 1000 (mloki per loki)
  autoThresholdLoki: number; // AUTO flips to ticker above this many units
  bech32Hrp: string; // informational in L1
  explorer: NativeAssetExplorer;
}

// The embedded Flokicoin default — mirrors internal/seed/seed.yaml native_asset.
export const FLOKICOIN_NATIVE_ASSET: NativeAssetFormat = {
  ticker: "FLC",
  symbol: "FLC",
  unitName: "loki",
  subunitName: "mloki",
  decimals: 8,
  subunitPerUnit: 1000,
  autoThresholdLoki: AUTO_UNIT_THRESHOLD_LOKI,
  bech32Hrp: "",
  explorer: {
    label: "Flokicoin Explorer",
    url: "https://flokichain.info",
    txPath: "/tx/{txid}",
  },
};

/**
 * Format an amount (in subunits, i.e. milliloki) for a native asset.
 * @param amount - Amount in subunits (milliloki)
 * @param displayFormat - Display format (required)
 * @param asset - The native-asset descriptor (defaults to Flokicoin)
 * @param showSymbol - Whether to show the symbol/unit
 */
export function formatNativeAmount(
  amount: number,
  displayFormat: FlokicoinDisplayFormat,
  asset: NativeAssetFormat = FLOKICOIN_NATIVE_ASSET,
  showSymbol: boolean = true
): string {
  const units = Math.floor(amount / asset.subunitPerUnit);

  const showTicker =
    displayFormat === FLOKICOIN_DISPLAY_FORMAT_FLC ||
    (displayFormat === FLOKICOIN_DISPLAY_FORMAT_AUTO &&
      units >= asset.autoThresholdLoki);

  if (showTicker) {
    const ticker = units / 10 ** asset.decimals;
    const formattedNumber = new Intl.NumberFormat(undefined, {
      minimumFractionDigits: 0,
      maximumFractionDigits: asset.decimals,
    }).format(ticker);
    return showSymbol ? `${formattedNumber} ${asset.ticker}` : formattedNumber;
  }
  const formattedNumber = new Intl.NumberFormat().format(units);
  return showSymbol ? `${formattedNumber} ${asset.unitName}` : formattedNumber;
}

/**
 * Utility function to format Flokicoin amounts as a string. Thin
 * backward-compatible wrapper over formatNativeAmount with the embedded
 * Flokicoin descriptor; call sites migrate to formatNativeAmount(useHubInfo()).
 * @param amount - Amount in milliloki
 * @param displayFormat - Display format (required)
 * @param showSymbol - Whether to show the symbol/unit
 */
export function formatFlokicoinAmount(
  amount: number,
  displayFormat: FlokicoinDisplayFormat,
  showSymbol: boolean = true
): string {
  return formatNativeAmount(
    amount,
    displayFormat,
    FLOKICOIN_NATIVE_ASSET,
    showSymbol
  );
}

/**
 * Returns the unit symbol/name based on display format and optional amount
 */
export function getFlokicoinUnit(
  displayFormat: FlokicoinDisplayFormat,
  amountLoki?: number
): string {
  let effectiveFormat = displayFormat;
  if (displayFormat === FLOKICOIN_DISPLAY_FORMAT_AUTO) {
    effectiveFormat =
      (amountLoki || 0) >= AUTO_UNIT_THRESHOLD_LOKI
        ? FLOKICOIN_DISPLAY_FORMAT_FLC
        : "loki";
  }

  if (effectiveFormat === FLOKICOIN_DISPLAY_FORMAT_FLC) {
    return "FLC";
  }
  return "loki";
}

/**
 * Converts from internal loki units to display units (FLC or loki)
 */
export function lokiToDisplay(
  amountLoki: number,
  displayFormat: FlokicoinDisplayFormat
): number {
  let effectiveFormat = displayFormat;
  if (displayFormat === FLOKICOIN_DISPLAY_FORMAT_AUTO) {
    effectiveFormat =
      amountLoki >= AUTO_UNIT_THRESHOLD_LOKI
        ? FLOKICOIN_DISPLAY_FORMAT_FLC
        : "loki";
  }

  if (effectiveFormat === FLOKICOIN_DISPLAY_FORMAT_FLC) {
    return amountLoki / 100_000_000;
  }
  return amountLoki;
}

/**
 * Converts from display units (FLC or loki) to internal loki units
 */
export function displayToLoki(
  amountDisplay: number,
  displayFormat: FlokicoinDisplayFormat
): number {
  // In AUTO mode, we assume the user entered the value in the "effective" unit currently shown.
  // This is handled by the caller usually, but for parsing we need to know if it's FLC or loki.
  // If the input was decimal, it's definitely FLC. If it's a large integer, it's tricky.
  // To keep it simple, if displayFormat is AUTO, we default to the same logic as lokiToDisplay
  // but this is mostly used for user input.

  if (displayFormat === FLOKICOIN_DISPLAY_FORMAT_FLC) {
    return Math.round(amountDisplay * 100_000_000);
  }

  // NOTE: If AUTO is used for input, it might be ambiguous.
  // However, input forms usually don't use AUTO for the Input itself, they use the current state.
  return Math.round(amountDisplay);
}
