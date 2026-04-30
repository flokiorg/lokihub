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
  
  const displayFormat = info?.flokicoinDisplayFormat || "auto";

  return {
    unit: (amountLoki?: number) => getFlokicoinUnit(displayFormat, amountLoki),
    scaleAmount: (amountLoki: number) => lokiToDisplay(amountLoki, displayFormat),
    parseAmount: (amountDisplay: number) => displayToLoki(amountDisplay, displayFormat),
    scaleInputAmount: (amountLoki: number, inputUnit: "FLC" | "loki") => lokiToDisplay(amountLoki, inputUnit === "FLC" ? "flc" : "loki"),
    parseInputAmount: (amountDisplay: number, inputUnit: "FLC" | "loki") => displayToLoki(amountDisplay, inputUnit === "FLC" ? "flc" : "loki"),
    displayFormat,
  };
}
