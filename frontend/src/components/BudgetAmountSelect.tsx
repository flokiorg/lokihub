import React from "react";
import { CurrencyInput } from "src/components/CurrencyInput";
import { Label } from "src/components/ui/label";
import { useUnit } from "src/hooks/useUnit";
import { cn } from "src/lib/utils";

function BudgetAmountSelect({
  value,
  onChange,
  minAmount,
}: {
  value: number;
  onChange: (value: number) => void;
  minAmount?: number;
}) {
  const { scaleInputAmount, parseInputAmount, displayFormat } = useUnit();
  
  const [inputUnit, setInputUnit] = React.useState<"FLC" | "loki">("FLC");
  React.useEffect(() => {
    if (displayFormat === "flc") setInputUnit("FLC");
    else if (displayFormat === "loki") setInputUnit("loki");
    else setInputUnit("FLC");
  }, [displayFormat]);

  const flcPresets = [21, 500, 2100];
  const lokiPresets = [21000, 500000, 21000000];

  const presets = inputUnit === "FLC" ? flcPresets : lokiPresets;

  const [customBudget, setCustomBudget] = React.useState(
    value ? ![...flcPresets, ...lokiPresets].some(p => parseInputAmount(p, inputUnit) === value) : false
  );

  const isActive = (preset: number) => {
    return !customBudget && value === parseInputAmount(preset, inputUnit);
  };

  return (
    <>
      <div className="grid grid-cols-2 md:grid-cols-5 gap-2 text-xs mb-4">
        {presets.map((preset) => {
            let label = preset.toString();
            if (inputUnit === "loki") {
                if (preset >= 1000000) label = (preset / 1000000) + "M";
                else if (preset >= 1000) label = (preset / 1000) + "k";
            }
            
            return (
              <button
                type="button"
                key={preset}
                onClick={() => {
                  setCustomBudget(false);
                  onChange(parseInputAmount(preset, inputUnit));
                }}
                className={cn(
                  "cursor-pointer rounded text-nowrap border-2 text-center p-2 py-4 slashed-zero transition-colors",
                  isActive(preset)
                    ? "border-primary bg-primary/5 text-primary"
                    : "border-muted hover:bg-accent"
                )}
              >
                <div className="font-medium">
                   {label}
                </div>
              </button>
            );
          })}

        <button
            type="button"
            onClick={() => {
                setCustomBudget(false);
                onChange(0);
            }}
            className={cn(
                "cursor-pointer rounded text-nowrap border-2 text-center p-2 py-4 transition-colors",
                !customBudget && value === 0
                    ? "border-primary bg-primary/5 text-primary"
                    : "border-muted hover:bg-accent"
            )}
        >
            <div className="font-medium text-xs">Unlimited</div>
        </button>

        <button
          type="button"
          onClick={() => {
            setCustomBudget(true);
            if (value === 0 || presets.some(p => parseInputAmount(p, inputUnit) === value)) {
                onChange(minAmount || (inputUnit === "FLC" ? 21 : 21000));
            }
          }}
          className={cn(
            "cursor-pointer rounded border-2 text-center p-4 transition-colors",
            customBudget ? "border-primary bg-primary/5 text-primary" : "border-muted hover:bg-accent"
          )}
        >
          Custom
        </button>
      </div>
      {customBudget && (
        <div className="grid gap-2 mb-5">
          <Label htmlFor="budget">Custom budget amount ({inputUnit})</Label>
          <CurrencyInput
            id="budget"
            amount={scaleInputAmount(value, inputUnit).toString()}
            onAmountChange={(val) => {
                onChange(parseInputAmount(parseFloat(val) || 0, inputUnit));
            }}
            inputUnit={inputUnit}
            onInputUnitChange={setInputUnit}
            required
            min={minAmount ? scaleInputAmount(minAmount, inputUnit) : 1}
          />
        </div>
      )}
    </>
  );
}

export default BudgetAmountSelect;
