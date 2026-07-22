import React from "react";
import { CurrencyInput } from "src/components/CurrencyInput";
import { DurationInput } from "src/components/DurationInput";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "src/components/ui/card";
import { Label } from "src/components/ui/label";
import { useInputUnit, useUnit } from "src/hooks/useUnit";

interface JITHubConfigCardProps {
  title?: React.ReactNode;
  description?: React.ReactNode;
  budgetLabel: React.ReactNode;
  budgetHelper: React.ReactNode;
  expiryLabel: React.ReactNode;
  expiryHelper: React.ReactNode;
  perWalletMaxLoki: number;
  onPerWalletMaxLokiChange: (loki: number) => void;
  maxExpSecs: number;
  onMaxExpSecsChange: (seconds: number) => void;
}

// Shared per-wallet-cap / max-expiry fields for a JIT Hub — the same two
// limits govern every jit_wallet a hub can ever issue, whether the hub is
// being created from Sub-wallets (NewJITHub), edited afterward (AppDetails'
// Hub Settings card), or created inline from the generic connect flow
// (NewApp). One definition keeps the fields/validation identical everywhere
// rather than re-implementing this pair three times.
export function JITHubConfigCard({
  title,
  description,
  budgetLabel,
  budgetHelper,
  expiryLabel,
  expiryHelper,
  perWalletMaxLoki,
  onPerWalletMaxLokiChange,
  maxExpSecs,
  onMaxExpSecsChange,
}: JITHubConfigCardProps) {
  const { scaleInputAmount, parseInputAmount } = useUnit();
  const [inputUnit, setInputUnit] = useInputUnit(perWalletMaxLoki);

  const fields = (
    <div className="w-full grid gap-4 max-w-lg">
      <div className="w-full grid gap-1.5">
        <Label htmlFor="jitPerWalletMax">{budgetLabel}</Label>
        <CurrencyInput
          id="jitPerWalletMax"
          amount={
            perWalletMaxLoki
              ? scaleInputAmount(perWalletMaxLoki, inputUnit).toString()
              : ""
          }
          onAmountChange={(val) =>
            onPerWalletMaxLokiChange(
              parseInputAmount(parseFloat(val) || 0, inputUnit)
            )
          }
          inputUnit={inputUnit}
          onInputUnitChange={setInputUnit}
          required
          min={1}
        />
        <p className="text-muted-foreground text-sm">{budgetHelper}</p>
      </div>
      <div className="w-full grid gap-1.5">
        <Label htmlFor="jitMaxExpSecs">{expiryLabel}</Label>
        <DurationInput
          id="jitMaxExpSecs"
          seconds={maxExpSecs}
          onChange={onMaxExpSecsChange}
          min={60}
        />
        <p className="text-muted-foreground text-sm">{expiryHelper}</p>
      </div>
    </div>
  );

  if (!title) {
    return fields;
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        {description && <CardDescription>{description}</CardDescription>}
      </CardHeader>
      <CardContent>{fields}</CardContent>
    </Card>
  );
}
