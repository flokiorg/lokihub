import { useHubInfo } from "src/hooks/useHubInfo";
import { useInfo } from "src/hooks/useInfo";
import {
  FLOKICOIN_NATIVE_ASSET,
  formatNativeAmount,
} from "src/utils/flokicoinFormatting";

interface FormattedFlokicoinAmountProps {
  amount: number; // Amount in milliloki
  className?: string;
  showSymbol?: boolean; // Whether to show the symbol/unit
}

/**
 * Formats an on-chain amount according to the user's display-format setting and
 * the hub's native asset (ticker/units/decimals from GET /api/hub/info). Falls
 * back to the embedded Flokicoin asset while the hub info is loading.
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
  const { data: hubInfo } = useHubInfo();

  if (!info) {
    return null;
  }

  const asset = hubInfo?.nativeAsset ?? FLOKICOIN_NATIVE_ASSET;
  const formatted = formatNativeAmount(
    amount,
    info.flokicoinDisplayFormat,
    asset,
    showSymbol
  );

  return <span className={className}>{formatted}</span>;
}
