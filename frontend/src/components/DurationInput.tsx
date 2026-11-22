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
  // enforces its own ceiling (e.g. a Cash Hub's max_exp_secs).
  max?: number;
  // presets overrides the default hour/day-scale preset buttons — used where
  // a caller operates on a longer timescale (e.g. a Circle Hub's
  // week/month/year-scale max wallet expiry).
  presets?: { label: string; seconds: number }[];
  // allowNever adds a "Never" button (0 seconds) alongside the presets —
  // used where 0 is a legitimate, meaningful value (e.g. a Cash Hub's
  // max_exp_secs, where 0 means "no ceiling at all"), as opposed to `min`
  // simply enforcing a floor on a real duration.
  allowNever?: boolean;
  neverLabel?: string;
}

export function DurationInput({
  id,
  seconds,
  onChange,
  min = 60,
  max,
  presets = PRESETS,
  allowNever = false,
  neverLabel = "Never",
}: DurationInputProps) {
  const isNever = allowNever && seconds === 0;
  const [unit, setUnit] = React.useState<DurationUnit>(() => pickUnit(seconds));
  const [amount, setAmount] = React.useState<string>(() =>
    seconds ? String(seconds / UNIT_SECONDS[pickUnit(seconds)]) : ""
  );
  const [showCustom, setShowCustom] = React.useState(
    () => !isNever && !presets.some((preset) => preset.seconds === seconds)
  );
  const inputRef = React.useRef<HTMLInputElement>(null);

  const activePreset = showCustom
    ? undefined
    : presets.find((preset) => preset.seconds === seconds);

  const applyAmount = (nextAmount: string, nextUnit: DurationUnit) => {
    const parsed = parseFloat(nextAmount);
    let nextSeconds =
      parsed > 0 ? Math.round(parsed * UNIT_SECONDS[nextUnit]) : 0;
    nextSeconds = Math.max(nextSeconds, nextSeconds ? min : 0);
    // Intentionally not clamped to max here — silently capping would submit
    // a different value than what's displayed. The caller shows an inline
    // "exceeds max" error instead (see CashHubAllocations.tsx) and disables
    // submit until the value is fixed.
    onChange(nextSeconds);
  };

  // A quiet chip: a single hairline border, a primary tint when active —
  // consistent with the status/language chips elsewhere, instead of the old
  // heavy 2px boxes. Wrapping (not a fixed 6-col grid) lets every preset show,
  // so none has to be hidden to fit the row.
  const chipClass = (active: boolean, disabled = false) =>
    cn(
      "rounded-md border px-3 py-1.5 text-nowrap text-center font-medium transition-colors",
      disabled
        ? "cursor-not-allowed border-input text-muted-foreground opacity-40"
        : active
          ? "border-primary/40 bg-primary/10 text-primary"
          : "cursor-pointer border-input text-muted-foreground hover:border-primary/40 hover:text-foreground"
    );

  return (
    <div className="grid gap-2">
      <div className="flex flex-wrap gap-2 text-xs">
        {presets.map((preset) => {
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
              className={chipClass(
                activePreset?.seconds === preset.seconds,
                exceedsMax
              )}
            >
              {preset.label}
            </button>
          );
        })}
        {allowNever && (
          <button
            type="button"
            onClick={() => {
              setShowCustom(false);
              onChange(0);
            }}
            className={chipClass(isNever)}
          >
            {neverLabel}
          </button>
        )}
        <button
          type="button"
          onClick={() => {
            setShowCustom(true);
            requestAnimationFrame(() => inputRef.current?.focus());
          }}
          className={chipClass(showCustom)}
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
                {(["minutes", "hours", "days"] as DurationUnit[]).map(
                  (option) => (
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
                  )
                )}
              </div>
            }
          />
        </div>
      )}
    </div>
  );
}
