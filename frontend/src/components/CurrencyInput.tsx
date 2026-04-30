import React from "react";
import { InputWithAdornment } from "src/components/ui/custom/input-with-adornment";
import { useUnit } from "src/hooks/useUnit";
import { cn } from "src/lib/utils";

interface CurrencyInputProps extends Omit<React.ComponentProps<"input">, "value" | "onChange"> {
  amount: string;
  onAmountChange: (val: string) => void;
  inputUnit: "FLC" | "loki";
  onInputUnitChange: (unit: "FLC" | "loki") => void;
}

export function CurrencyInput({ amount, onAmountChange, inputUnit, onInputUnitChange, ...props }: CurrencyInputProps) {
  const { displayFormat } = useUnit();

  const Adornment = () => {
    if (displayFormat !== "auto") {
      return <span className="mr-3 text-muted-foreground text-sm font-medium">{displayFormat === "flc" ? "FLC" : "loki"}</span>;
    }


    return (
      <div className="flex items-center bg-muted rounded-md p-0.5 mr-1 border z-10">
        <button
          type="button"
          className={cn(
            "px-2.5 py-1 rounded-sm text-xs font-medium transition-colors",
            inputUnit === "FLC" ? "bg-background shadow-sm text-foreground" : "text-muted-foreground hover:text-foreground"
          )}
          onClick={(e) => {
            e.preventDefault();
            onInputUnitChange("FLC");
          }}
        >
          FLC
        </button>
        <button
          type="button"
          className={cn(
            "px-2.5 py-1 rounded-sm text-xs font-medium transition-colors",
            inputUnit === "loki" ? "bg-background shadow-sm text-foreground" : "text-muted-foreground hover:text-foreground"
          )}
          onClick={(e) => {
            e.preventDefault();
            onInputUnitChange("loki");
          }}
        >
          loki
        </button>
      </div>
    );
  };

  return (
    <InputWithAdornment
      type="number"
      step="any"
      value={amount}
      onChange={(e) => onAmountChange(e.target.value.trim())}
      endAdornment={<Adornment />}
      {...props}
    />
  );
}
