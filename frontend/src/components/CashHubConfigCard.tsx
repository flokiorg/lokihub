import React from "react";
import { CurrencyInput } from "src/components/CurrencyInput";
import { DurationInput } from "src/components/DurationInput";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "src/components/ui/card";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { useInputUnit, useUnit } from "src/hooks/useUnit";

interface CashHubConfigCardProps {
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
  // minTransferLoki/onMinTransferLokiChange are optional — omit both to hide
  // the field entirely (used by the lightweight inline "Cash Hub (optional)"
  // escalation in the generic connect-app flow, which keeps only the two
  // original fields). 0 is a valid, submittable value ("no floor"), unlike
  // perWalletMaxLoki/maxExpSecs which both require a positive value.
  minTransferLoki?: number;
  onMinTransferLokiChange?: (loki: number) => void;
  // redeemFeePpm/onRedeemFeePpmChange follow the same optional-pair
  // convention as minTransferLoki above — omit both to hide the field (the
  // lightweight inline "Cash Hub (optional)" escalation keeps only the two
  // original fields; the full NewCashHub/Hub Settings flows always pass it).
  redeemFeePpm?: number;
  onRedeemFeePpmChange?: (ppm: number) => void;
}

// Shared per-wallet-cap / max-expiry / min-transfer fields for a Cash Hub —
// the same limits govern every cash_wallet a hub can ever mint cash into, whether the
// hub is being created from Sub-wallets (NewCashHub), edited afterward
// (AppDetails' Hub Settings card), or created inline from the generic
// connect flow (NewApp). One definition keeps the fields/validation
// identical everywhere rather than re-implementing this three times.
export function CashHubConfigCard({
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
  minTransferLoki,
  onMinTransferLokiChange,
  redeemFeePpm,
  onRedeemFeePpmChange,
}: CashHubConfigCardProps) {
  const { scaleInputAmount, parseInputAmount } = useUnit();
  const [inputUnit, setInputUnit] = useInputUnit(perWalletMaxLoki);

  const fields = (
    <div className="w-full grid gap-4 max-w-lg">
      <div className="w-full grid gap-1.5">
        <Label htmlFor="cashPerWalletMax">{budgetLabel}</Label>
        <CurrencyInput
          id="cashPerWalletMax"
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
        <Label htmlFor="cashMaxExpSecs">{expiryLabel}</Label>
        <DurationInput
          id="cashMaxExpSecs"
          seconds={maxExpSecs}
          onChange={onMaxExpSecsChange}
          min={60}
          allowNever
        />
        <p className="text-muted-foreground text-sm">{expiryHelper}</p>
      </div>
      {onMinTransferLokiChange && (
        <div className="w-full grid gap-1.5">
          <Label htmlFor="cashMinTransferMax">Min Transfer Amount</Label>
          <CurrencyInput
            id="cashMinTransferMax"
            amount={
              minTransferLoki
                ? scaleInputAmount(minTransferLoki, inputUnit).toString()
                : ""
            }
            onAmountChange={(val) =>
              onMinTransferLokiChange(
                parseInputAmount(parseFloat(val) || 0, inputUnit)
              )
            }
            inputUnit={inputUnit}
            onInputUnitChange={setInputUnit}
            min={0}
          />
          <p className="text-muted-foreground text-sm">
            Smallest amount a lokicash may be split into, or leave behind as
            change — 0 means no floor
          </p>
        </div>
      )}
      {onRedeemFeePpmChange && (
        <div className="w-full grid gap-1.5">
          <Label htmlFor="cashRedeemFeePpm">Redeem Fee (ppm)</Label>
          <Input
            id="cashRedeemFeePpm"
            type="number"
            min={0}
            value={redeemFeePpm ?? 0}
            onChange={(e) => onRedeemFeePpmChange(Number(e.target.value))}
          />
          <p className="text-muted-foreground text-sm">
            Per-million fee charged when a recipient redeems a lokicash out to
            an external wallet (0 = free). Never charged on a transfer/split,
            or on a redemption into another wallet on this same node.
          </p>
        </div>
      )}
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
