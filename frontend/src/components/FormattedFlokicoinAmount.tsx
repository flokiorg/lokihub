import { FLOKICOIN_DISPLAY_FORMAT_AUTO, FLOKICOIN_DISPLAY_FORMAT_FLC } from "src/constants";
import { useInfo } from "src/hooks/useInfo";
import { getFlokicoinUnit } from "src/utils/flokicoinFormatting";

interface FormattedFlokicoinAmountProps {
  amount: number; // Amount in milliloki
  className?: string;
  showSymbol?: boolean; // Whether to show the symbol/unit
}

/**
 * Formats a Flokicoin amount according to user settings
 * @param amount - Amount in milliloki
 * @param className - Optional CSS classes
 * @param showSymbol - Whether to show the symbol/unit (default: true)
 */
export function FormattedFlokicoinAmount({
  amount,
  className = "",
  showSymbol = true,
}: FormattedFlokicoinAmountProps) {
  const { data: info } = useInfo();

  if (!info) {
    return null;
  }

  // Convert from milliloki to loki
  const loki = Math.floor(amount / 1000);

  // Get display format from settings
  const displayFormat = info.flokicoinDisplayFormat;

  const unit = getFlokicoinUnit(displayFormat, loki);

  if (displayFormat === FLOKICOIN_DISPLAY_FORMAT_FLC || (displayFormat === FLOKICOIN_DISPLAY_FORMAT_AUTO && unit === "FLC")) {
    const flc = loki / 100_000_000;
    const formattedNumber = new Intl.NumberFormat(undefined, {
      minimumFractionDigits: 0,
      maximumFractionDigits: 8,
    }).format(flc);
    
    if (!showSymbol) {
        return <span className={className}>{formattedNumber}</span>;
    }
    return <span className={className}>{unit} {formattedNumber}</span>;
  }

  const formattedNumber = new Intl.NumberFormat().format(loki);

  if (!showSymbol) {
    return <span className={className}>{formattedNumber}</span>;
  }

  return <span className={className}>{formattedNumber} {unit}</span>;
}
