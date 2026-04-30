import { FLOKICOIN_DISPLAY_FORMAT_AUTO, FLOKICOIN_DISPLAY_FORMAT_FLC } from "src/constants";
import { FlokicoinDisplayFormat } from "src/types";

// 100,000 loki (0.001 FLC) threshold for AUTO mode
const AUTO_UNIT_THRESHOLD_LOKI = 100_000;

/**
 * Utility function to format Flokicoin amounts as a string
 * @param amount - Amount in milliloki
 * @param displayFormat - Display format (required)
 * @param showSymbol - Whether to show the symbol/unit
 */
export function formatFlokicoinAmount(
  amount: number,
  displayFormat: FlokicoinDisplayFormat,
  showSymbol: boolean = true
): string {
  const loki = Math.floor(amount / 1000);
  
  let effectiveFormat = displayFormat;
  if (displayFormat === FLOKICOIN_DISPLAY_FORMAT_AUTO) {
    effectiveFormat = loki >= AUTO_UNIT_THRESHOLD_LOKI ? FLOKICOIN_DISPLAY_FORMAT_FLC : "loki";
  }

  if (effectiveFormat === FLOKICOIN_DISPLAY_FORMAT_FLC) {
    const flc = loki / 100_000_000;
    const formattedNumber = new Intl.NumberFormat(undefined, {
        minimumFractionDigits: 0,
        maximumFractionDigits: 8,
      }).format(flc);
    return showSymbol ? `${formattedNumber} FLC` : formattedNumber;
  } else {
    const formattedNumber = new Intl.NumberFormat().format(loki);
    return showSymbol ? `${formattedNumber} loki` : formattedNumber;
  }
}

/**
 * Returns the unit symbol/name based on display format and optional amount
 */
export function getFlokicoinUnit(displayFormat: FlokicoinDisplayFormat, amountLoki?: number): string {
  let effectiveFormat = displayFormat;
  if (displayFormat === FLOKICOIN_DISPLAY_FORMAT_AUTO) {
    effectiveFormat = (amountLoki || 0) >= AUTO_UNIT_THRESHOLD_LOKI ? FLOKICOIN_DISPLAY_FORMAT_FLC : "loki";
  }

  if (effectiveFormat === FLOKICOIN_DISPLAY_FORMAT_FLC) {
    return "FLC";
  }
  return "loki";
}

/**
 * Converts from internal loki units to display units (FLC or loki)
 */
export function lokiToDisplay(amountLoki: number, displayFormat: FlokicoinDisplayFormat): number {
  let effectiveFormat = displayFormat;
  if (displayFormat === FLOKICOIN_DISPLAY_FORMAT_AUTO) {
    effectiveFormat = amountLoki >= AUTO_UNIT_THRESHOLD_LOKI ? FLOKICOIN_DISPLAY_FORMAT_FLC : "loki";
  }

  if (effectiveFormat === FLOKICOIN_DISPLAY_FORMAT_FLC) {
    return amountLoki / 100_000_000;
  }
  return amountLoki;
}

/**
 * Converts from display units (FLC or loki) to internal loki units
 */
export function displayToLoki(amountDisplay: number, displayFormat: FlokicoinDisplayFormat): number {
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
