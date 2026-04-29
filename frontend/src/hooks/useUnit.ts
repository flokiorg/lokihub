import { useInfo } from "src/hooks/useInfo";
import {
  displayToLoki,
  getFlokicoinUnit,
  lokiToDisplay,
} from "src/utils/flokicoinFormatting";

/**
 * Hook to get the current Flokicoin unit and conversion functions
 */
export function useUnit() {
  const { data: info } = useInfo();
  
  const displayFormat = info?.flokicoinDisplayFormat || "loki";

  return {
    unit: (amountLoki?: number) => getFlokicoinUnit(displayFormat, amountLoki),
    scaleAmount: (amountLoki: number) => lokiToDisplay(amountLoki, displayFormat),
    parseAmount: (amountDisplay: number) => displayToLoki(amountDisplay, displayFormat),
    displayFormat,
  };
}
