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
      return <span className="me-3 text-muted-foreground text-sm font-medium">{displayFormat === "flc" ? "FLC" : "loki"}</span>;
    }


    return (
      <div className="flex items-center bg-muted rounded-md p-0.5 me-1 border z-10">
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

  const handleAmountChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    // Strip everything except digits and decimal point
    let rawValue = e.target.value.replace(/[^0-9.]/g, "");
    
    // Prevent multiple decimal points
    const parts = rawValue.split(".");
    if (parts.length > 2) {
      rawValue = parts[0] + "." + parts.slice(1).join("");
    }

    onAmountChange(rawValue);
  };

  let displayValue = amount;
  if (amount) {
    const parts = amount.split(".");
    parts[0] = parts[0].replace(/\B(?=(\d{3})+(?!\d))/g, ",");
    displayValue = parts.join(".");
  }

  return (
    <InputWithAdornment
      type="text"
      dir="ltr"
      inputMode="decimal"
      value={displayValue}
      onChange={handleAmountChange}
      endAdornment={<Adornment />}
      {...props}
    />
  );
}
