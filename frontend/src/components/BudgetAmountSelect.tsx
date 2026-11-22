import { CurrencyInput } from "src/components/CurrencyInput";
import { Label } from "src/components/ui/label";
import { useInputUnit, useUnit } from "src/hooks/useUnit";
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
  const { scaleInputAmount, parseInputAmount } = useUnit();

  const [inputUnit, setInputUnit] = useInputUnit(value || undefined);

  const flcPresets = [21, 500, 2100];
  const lokiPresets = [21000, 500000, 21000000];

  const presets = inputUnit === "ticker" ? flcPresets : lokiPresets;

  // Whether the current value matches a preset (fills the input)
  const isPresetActive = (preset: number) =>
    value === parseInputAmount(preset, inputUnit);

  const isUnlimited = value === 0;

  return (
    <>
      <div className="grid grid-cols-2 md:grid-cols-4 gap-2 text-xs mb-4">
        {presets.map((preset) => {
          let label = preset.toString();
          if (inputUnit === "unit") {
            if (preset >= 1000000) label = preset / 1000000 + "M";
            else if (preset >= 1000) label = preset / 1000 + "k";
          }

          return (
            <button
              type="button"
              key={preset}
              onClick={() => {
                onChange(parseInputAmount(preset, inputUnit));
              }}
              className={cn(
                "cursor-pointer rounded-md border p-2.5 text-center font-medium text-nowrap slashed-zero transition-colors",
                isPresetActive(preset)
                  ? "border-primary/40 bg-primary/10 text-primary"
                  : "border-input text-muted-foreground hover:border-primary/40 hover:text-foreground"
              )}
            >
              {label}
            </button>
          );
        })}

        <button
          type="button"
          onClick={() => {
            onChange(0);
          }}
          className={cn(
            "cursor-pointer rounded-md border p-2.5 text-center font-medium text-nowrap transition-colors",
            isUnlimited
              ? "border-primary/40 bg-primary/10 text-primary"
              : "border-input text-muted-foreground hover:border-primary/40 hover:text-foreground"
          )}
        >
          Unlimited
        </button>
      </div>

      {/* Amount input is always shown unless Unlimited is selected */}
      {!isUnlimited && (
        <div className="grid gap-2 mb-5">
          <Label htmlFor="budget">Budget amount</Label>
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
