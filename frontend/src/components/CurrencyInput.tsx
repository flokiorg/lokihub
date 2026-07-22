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

export function CurrencyInput({ amount, onAmountChange, inputUnit, onInputUnitChange, onFocus, onBlur, ...props }: CurrencyInputProps) {
  const { displayFormat } = useUnit();
  // Callers typically derive `amount` from a parsed number (e.g.
  // scaleInputAmount(loki, unit).toString()), which round-trips through
  // parseFloat on every keystroke. Deleting down to something like "0.0000"
  // parses to the number 0, and callers' `amount ? ... : ""` ternaries then
  // render "" — wiping the field mid-edit. Keeping our own text buffer while
  // focused avoids letting that recomputed prop clobber in-progress typing;
  // we only resync from `amount` when the field isn't actively being edited.
  const [text, setText] = React.useState(amount);
  const isFocusedRef = React.useRef(false);

  React.useEffect(() => {
    if (!isFocusedRef.current) {
      setText(amount);
    }
  }, [amount]);

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

    setText(rawValue);
    onAmountChange(rawValue);
  };

  let displayValue = text;
  if (text) {
    const parts = text.split(".");
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
      onFocus={(e) => {
        isFocusedRef.current = true;
        onFocus?.(e);
      }}
      onBlur={(e) => {
        isFocusedRef.current = false;
        setText(amount);
        onBlur?.(e);
      }}
      endAdornment={<Adornment />}
      {...props}
    />
  );
}
