import React from "react";
import { InputWithAdornment } from "src/components/ui/custom/input-with-adornment";
import { cn } from "src/lib/utils";

type DurationUnit = "minutes" | "hours" | "days";

const UNIT_SECONDS: Record<DurationUnit, number> = {
  minutes: 60,
  hours: 3600,
  days: 86400,
};

const PRESETS: { label: string; seconds: number }[] = [
  { label: "1 hour", seconds: 3600 },
  { label: "6 hours", seconds: 6 * 3600 },
  { label: "1 day", seconds: 86400 },
  { label: "3 days", seconds: 3 * 86400 },
  { label: "7 days", seconds: 7 * 86400 },
  { label: "30 days", seconds: 30 * 86400 },
];

const pickUnit = (seconds: number): DurationUnit => {
  if (seconds !== 0 && seconds % 86400 === 0) {
    return "days";
  }
  if (seconds !== 0 && seconds % 3600 === 0) {
    return "hours";
  }
  return "minutes";
};

interface DurationInputProps {
  id?: string;
  seconds: number;
  onChange: (seconds: number) => void;
  min?: number;
  // max disables presets/typed values above it — used where a caller
  // enforces its own ceiling (e.g. a JIT Hub's max_exp_secs).
  max?: number;
  // presets overrides the default hour/day-scale preset buttons — used where
  // a caller operates on a longer timescale (e.g. a Circle Hub's
  // week/month/year-scale max wallet expiry).
  presets?: { label: string; seconds: number }[];
}

export function DurationInput({ id, seconds, onChange, min = 60, max, presets = PRESETS }: DurationInputProps) {
  const [unit, setUnit] = React.useState<DurationUnit>(() => pickUnit(seconds));
  const [amount, setAmount] = React.useState<string>(() =>
    seconds ? String(seconds / UNIT_SECONDS[pickUnit(seconds)]) : ""
  );
  const [showCustom, setShowCustom] = React.useState(
    () => !presets.some((preset) => preset.seconds === seconds)
  );
  const inputRef = React.useRef<HTMLInputElement>(null);

  const activePreset = showCustom ? undefined : presets.find((preset) => preset.seconds === seconds);

  const applyAmount = (nextAmount: string, nextUnit: DurationUnit) => {
    const parsed = parseFloat(nextAmount);
    let nextSeconds = parsed > 0 ? Math.round(parsed * UNIT_SECONDS[nextUnit]) : 0;
    nextSeconds = Math.max(nextSeconds, nextSeconds ? min : 0);
    // Intentionally not clamped to max here — silently capping would submit
    // a different value than what's displayed. The caller shows an inline
    // "exceeds max" error instead (see JITHubAllocations.tsx) and disables
    // submit until the value is fixed.
    onChange(nextSeconds);
  };

  return (
    <div className="grid gap-2">
      <div className="grid grid-cols-3 sm:grid-cols-6 gap-2 text-xs">
        {presets.slice(0, -1).map((preset) => {
          const exceedsMax = max !== undefined && preset.seconds > max;
          return (
            <button
              type="button"
              key={preset.label}
              disabled={exceedsMax}
              onClick={() => {
                setShowCustom(false);
                onChange(preset.seconds);
                const nextUnit = pickUnit(preset.seconds);
                setUnit(nextUnit);
                setAmount(String(preset.seconds / UNIT_SECONDS[nextUnit]));
              }}
              className={cn(
                "cursor-pointer rounded text-nowrap border-2 text-center p-2.5",
                exceedsMax && "cursor-not-allowed opacity-40",
                activePreset?.seconds === preset.seconds
                  ? "border-primary"
                  : "border-muted"
              )}
            >
              {preset.label}
            </button>
          );
        })}
        <button
          type="button"
          onClick={() => {
            setShowCustom(true);
            requestAnimationFrame(() => inputRef.current?.focus());
          }}
          className={cn(
            "cursor-pointer rounded text-nowrap border-2 text-center p-2.5",
            showCustom ? "border-primary" : "border-muted"
          )}
        >
          Custom
        </button>
      </div>
      {showCustom && (
        <div className="flex items-center gap-2">
          <InputWithAdornment
            ref={inputRef}
            id={id}
            type="number"
            inputMode="decimal"
            min={1}
            step="any"
            placeholder="Custom duration"
            value={amount}
            onChange={(e) => {
              setAmount(e.target.value);
              applyAmount(e.target.value, unit);
            }}
            endAdornment={
              <div className="flex items-center bg-muted rounded-md p-0.5 me-1 border z-10">
                {(["minutes", "hours", "days"] as DurationUnit[]).map((option) => (
                  <button
                    key={option}
                    type="button"
                    className={cn(
                      "px-2.5 py-1 rounded-sm text-xs font-medium capitalize transition-colors",
                      unit === option
                        ? "bg-background shadow-sm text-foreground"
                        : "text-muted-foreground hover:text-foreground"
                    )}
                    onClick={() => {
                      setUnit(option);
                      applyAmount(amount, option);
                    }}
                  >
                    {option}
                  </button>
                ))}
              </div>
            }
          />
        </div>
      )}
    </div>
  );
}
