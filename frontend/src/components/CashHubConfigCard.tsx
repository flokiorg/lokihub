import React from "react";
import { useTranslation } from "react-i18next";
import { CurrencyInput } from "src/components/CurrencyInput";
import { DurationInput } from "src/components/DurationInput";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "src/components/ui/accordion";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { WEEK_SCALE_PRESETS } from "src/constants";
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
  // original fields). 0 is a valid, submittable value ("no floor") — as it
  // is for maxExpSecs, where 0 means "never" (hence allowNever on its
  // DurationInput below, and NIP-CASH §Data Model's explicit allowance).
  // Only perWalletMaxLoki genuinely requires a positive value: a per-wallet
  // cap has no "unlimited" mode.
  minTransferLoki?: number;
  onMinTransferLokiChange?: (loki: number) => void;
  // redeemFeePpm/onRedeemFeePpmChange follow the same optional-pair
  // convention as minTransferLoki above — omit both to hide the field (the
  // lightweight inline "Cash Hub (optional)" escalation keeps only the two
  // original fields; the full NewCashHub/Hub Settings flows always pass it).
  redeemFeePpm?: number;
  onRedeemFeePpmChange?: (ppm: number) => void;
  // spentRetentionSecs/onSpentRetentionSecsChange follow the same optional-pair
  // convention again, and additionally gate the whole Advanced disclosure:
  // omit them and the section does not render at all, which is what the
  // lightweight inline escalation in NewApp wants.
  //
  // 0 is meaningful ("Disabled" — the hub falls silent the moment a bill is
  // destroyed, the behaviour that predates the tombstone), which is why the
  // DurationInput below is given allowNever.
  spentRetentionSecs?: number;
  onSpentRetentionSecsChange?: (seconds: number) => void;
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
  spentRetentionSecs,
  onSpentRetentionSecsChange,
}: CashHubConfigCardProps) {
  const { t } = useTranslation("apps");
  const { scaleInputAmount, parseInputAmount } = useUnit();
  const [inputUnit, setInputUnit] = useInputUnit(perWalletMaxLoki);

  // The per-bill cap and expiry are the two limits almost everyone sets, so
  // they stay prominent. Min-transfer, redeem-fee and spent-bill retention are
  // economics/policy knobs with good defaults, so they live together under one
  // Advanced disclosure (rendered only when its owner passes the handlers).
  const hasAdvanced =
    !!onMinTransferLokiChange ||
    !!onRedeemFeePpmChange ||
    !!onSpentRetentionSecsChange;

  const fields = (
    <div className="w-full grid gap-4">
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
      {hasAdvanced && (
        <Accordion type="single" collapsible>
          <AccordionItem value="advanced" className="border-b-0">
            <AccordionTrigger className="py-2 text-sm font-medium">
              {t("circleHub.advancedTitle")}
            </AccordionTrigger>
            <AccordionContent className="pb-0">
              <div className="w-full grid gap-4">
                {onMinTransferLokiChange && (
                  <div className="w-full grid gap-1.5">
                    <Label htmlFor="cashMinTransferMax">
                      {t("circleHub.cashMinTransferLabel")}
                    </Label>
                    <CurrencyInput
                      id="cashMinTransferMax"
                      amount={
                        minTransferLoki
                          ? scaleInputAmount(
                              minTransferLoki,
                              inputUnit
                            ).toString()
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
                      {t("circleHub.cashMinTransferHelper")}
                    </p>
                  </div>
                )}
                {onRedeemFeePpmChange && (
                  <div className="w-full grid gap-1.5">
                    <Label htmlFor="cashRedeemFeePpm">
                      {t("circleHub.cashRedeemFeeLabel")}
                    </Label>
                    <Input
                      id="cashRedeemFeePpm"
                      type="number"
                      min={0}
                      value={redeemFeePpm ?? 0}
                      onChange={(e) =>
                        onRedeemFeePpmChange(Number(e.target.value))
                      }
                    />
                    <p className="text-muted-foreground text-sm">
                      {t("circleHub.cashRedeemFeeHelper")}
                    </p>
                  </div>
                )}
                {onSpentRetentionSecsChange && (
                  <div className="w-full grid gap-1.5">
                    <Label htmlFor="cashSpentRetentionSecs">
                      {t("circleHub.spentRetentionLabel")}
                    </Label>
                    <DurationInput
                      id="cashSpentRetentionSecs"
                      seconds={spentRetentionSecs ?? 0}
                      onChange={onSpentRetentionSecsChange}
                      min={60}
                      presets={WEEK_SCALE_PRESETS}
                      allowNever
                    />
                    <p className="text-muted-foreground text-sm">
                      {t("circleHub.spentRetentionHelper")}
                    </p>
                  </div>
                )}
              </div>
            </AccordionContent>
          </AccordionItem>
        </Accordion>
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
