import React from "react";
import { localStorageKeys } from "src/constants";
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

/**
 * Local unit toggle for an editable currency field. Reuses the user's last
 * explicit FLC/loki choice (persisted in localStorage) across every field
 * using this hook; falls back to the auto-threshold logic (via
 * getFlokicoinUnit) or FLC on first use, and forces the choice to match the
 * global setting whenever that setting isn't "auto".
 */
export function useInputUnit(referenceAmountLoki: number | undefined) {
  const { displayFormat } = useUnit();
  const [inputUnit, setInputUnitState] = React.useState<"FLC" | "loki">(() => {
    const stored = localStorage.getItem(localStorageKeys.preferredInputUnit);
    if (stored === "FLC" || stored === "loki") {
      return stored;
    }
    if (referenceAmountLoki === undefined) {
      return "FLC";
    }
    return getFlokicoinUnit(displayFormat, referenceAmountLoki) as "FLC" | "loki";
  });

  const setInputUnit = React.useCallback((unit: "FLC" | "loki") => {
    setInputUnitState(unit);
    localStorage.setItem(localStorageKeys.preferredInputUnit, unit);
  }, []);

  React.useEffect(() => {
    if (displayFormat === "flc") {
      setInputUnitState("FLC");
    } else if (displayFormat === "loki") {
      setInputUnitState("loki");
    }
  }, [displayFormat]);

  return [inputUnit, setInputUnit] as const;
}
