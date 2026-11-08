import {
  FLOKICOIN_DISPLAY_FORMAT_AUTO,
  FLOKICOIN_DISPLAY_FORMAT_FLC,
} from "src/constants";
import { FlokicoinDisplayFormat } from "src/types";

// 100,000 loki (0.001 FLC) threshold for AUTO mode
const AUTO_UNIT_THRESHOLD_LOKI = 100_000;

// NativeAssetFormat describes how to render the hub's on-chain asset. It
// mirrors the backend seed.NativeAsset block (GET /api/hub/info -> nativeAsset)
// so display is driven from one source of truth. The embedded default is
// Flokicoin; pass a descriptor from useHubInfo() to render another chain.
export interface NativeAssetFormat {
  ticker: string; // unit of account shown to users, e.g. "FLC"
  unitName: string; // base on-chain unit, e.g. "loki"
  subunitPerUnit: number; // subunits (milliloki) per unit, e.g. 1000
  unitPerTicker: number; // units per ticker, e.g. 100_000_000 (1 FLC = 1e8 loki)
  autoThresholdUnit: number; // AUTO flips to ticker above this many units
  tickerFractionDigits?: number; // max fraction digits in ticker form (default 8)
}

// The embedded Flokicoin default — keep in sync with internal/seed/seed.yaml.
export const FLOKICOIN_NATIVE_ASSET: NativeAssetFormat = {
  ticker: "FLC",
  unitName: "loki",
  subunitPerUnit: 1000,
  unitPerTicker: 100_000_000,
  autoThresholdUnit: AUTO_UNIT_THRESHOLD_LOKI,
  tickerFractionDigits: 8,
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

  let effectiveFormat = displayFormat;
  if (displayFormat === FLOKICOIN_DISPLAY_FORMAT_AUTO) {
    effectiveFormat =
      units >= asset.autoThresholdUnit
        ? FLOKICOIN_DISPLAY_FORMAT_FLC
        : asset.unitName;
  }

  if (effectiveFormat === FLOKICOIN_DISPLAY_FORMAT_FLC) {
    const ticker = units / asset.unitPerTicker;
    const formattedNumber = new Intl.NumberFormat(undefined, {
      minimumFractionDigits: 0,
      maximumFractionDigits: asset.tickerFractionDigits ?? 8,
    }).format(ticker);
    return showSymbol ? `${formattedNumber} ${asset.ticker}` : formattedNumber;
  } else {
    const formattedNumber = new Intl.NumberFormat().format(units);
    return showSymbol ? `${formattedNumber} ${asset.unitName}` : formattedNumber;
  }
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
