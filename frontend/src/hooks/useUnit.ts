import React from "react";
import { localStorageKeys } from "src/constants";
import { useHubInfo } from "src/hooks/useHubInfo";
import { useInfo } from "src/hooks/useInfo";
import {
  FLOKICOIN_NATIVE_ASSET,
  displayToLoki,
  getFlokicoinUnit,
  lokiToDisplay,
} from "src/utils/flokicoinFormatting";

/**
 * InputUnit is the user's persisted toggle for an editable amount field:
 * "ticker" edits in the unit of account (FLC by default), "unit" edits in the
 * base on-chain unit (loki by default). These are semantic discriminants — the
 * labels actually shown come from the configured native asset
 * (useHubInfo().nativeAsset), never from the chain-identity strings here.
 */
export type InputUnit = "ticker" | "unit";

/**
 * Hook to get the current Flokicoin unit and conversion functions
 */
export function useUnit() {
  const { data: info } = useInfo();
  const { data: hubInfo } = useHubInfo();

  const displayFormat = info?.flokicoinDisplayFormat || "auto";
  const asset = hubInfo?.nativeAsset ?? FLOKICOIN_NATIVE_ASSET;

  return {
    unit: (amountLoki?: number) =>
      getFlokicoinUnit(displayFormat, amountLoki, asset),
    scaleAmount: (amountLoki: number) =>
      lokiToDisplay(amountLoki, displayFormat, asset),
    parseAmount: (amountDisplay: number) =>
      displayToLoki(amountDisplay, displayFormat, asset),
    // Resolve an InputUnit discriminant to the label actually shown to the
    // user, from the configured native asset (FLC/loki by default). Display
    // sites must render this, never the raw "ticker"/"unit" discriminant.
    inputUnitLabel: (inputUnit: InputUnit) =>
      inputUnit === "ticker" ? asset.ticker : asset.unitName,
    scaleInputAmount: (amountLoki: number, inputUnit: InputUnit) =>
      lokiToDisplay(amountLoki, inputUnit === "ticker" ? "flc" : "loki", asset),
    parseInputAmount: (amountDisplay: number, inputUnit: InputUnit) =>
      displayToLoki(
        amountDisplay,
        inputUnit === "ticker" ? "flc" : "loki",
        asset
      ),
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
  const [inputUnit, setInputUnitState] = React.useState<InputUnit>(() => {
    const stored = localStorage.getItem(localStorageKeys.preferredInputUnit);
    if (stored === "ticker" || stored === "unit") {
      return stored;
    }
    if (referenceAmountLoki === undefined) {
      return "ticker";
    }
    return getFlokicoinUnit(displayFormat, referenceAmountLoki) ===
      FLOKICOIN_NATIVE_ASSET.ticker
      ? "ticker"
      : "unit";
  });

  const setInputUnit = React.useCallback((unit: InputUnit) => {
    setInputUnitState(unit);
    localStorage.setItem(localStorageKeys.preferredInputUnit, unit);
  }, []);

  React.useEffect(() => {
    if (displayFormat === "flc") {
      setInputUnitState("ticker");
    } else if (displayFormat === "loki") {
      setInputUnitState("unit");
    }
  }, [displayFormat]);

  return [inputUnit, setInputUnit] as const;
}
