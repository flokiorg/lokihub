import { FLOKICOIN_DISPLAY_FORMAT_BIP177 } from "src/constants";
import { FlokicoinDisplayFormat } from "src/types";

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
  const formattedNumber = new Intl.NumberFormat().format(loki);

  if (!showSymbol) {
    return formattedNumber;
  }

  if (displayFormat === FLOKICOIN_DISPLAY_FORMAT_BIP177) {
    return `FLC ${formattedNumber}`;
  } else {
    return `${formattedNumber} loki`;
  }
}
