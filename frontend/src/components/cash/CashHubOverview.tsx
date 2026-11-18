import {
  CircleMinusIcon,
  CirclePlusIcon,
  InfoIcon,
  ShieldCheckIcon,
  TriangleAlertIcon,
} from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { IsolatedAppDrawDownDialog } from "src/components/IsolatedAppDrawDownDialog";
import { IsolatedAppTopupDialog } from "src/components/IsolatedAppTopupDialog";
import { Button } from "src/components/ui/button";
import { Card, CardContent } from "src/components/ui/card";
import { Progress } from "src/components/ui/progress";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "src/components/ui/tooltip";
import { cn } from "src/lib/utils";
import { getBudgetRenewalLabel } from "src/lib/utils";
import { App, CashHubStats } from "src/types";

// Returns the unit key and count rather than a translated string: i18n keys
// here are strongly typed, so `t` cannot be passed around as a loose
// (key, options) => string without losing that checking.
function durationParts(secs: number): {
  unit: "Seconds" | "Minutes" | "Hours" | "Days";
  count: number;
} {
  if (secs < 60) {
    return { unit: "Seconds", count: secs };
  }
  if (secs < 3600) {
    return { unit: "Minutes", count: Math.round(secs / 60) };
  }
  if (secs < 86400) {
    return { unit: "Hours", count: Math.round(secs / 3600) };
  }
  return { unit: "Days", count: Math.round(secs / 86400) };
}

// Exported so the Cash Hubs list can render the same figures for every hub
// at once without a second, drifting copy of them.
export function MedianDuration({ secs }: { secs: number }) {
  const { t } = useTranslation("circles");
  const { unit, count } = durationParts(secs);
  switch (unit) {
    case "Seconds":
      return <>{t("cashKpis.durationSeconds", { count })}</>;
    case "Minutes":
      return <>{t("cashKpis.durationMinutes", { count })}</>;
    case "Hours":
      return <>{t("cashKpis.durationHours", { count })}</>;
    default:
      return <>{t("cashKpis.durationDays", { count })}</>;
  }
}

// The (i) in front of a metric's label. Every figure on this card is a
// different slice of the same money, and several of them ("Outstanding",
// "Returned", "Split") only make sense once you know which slice — so the
// definition travels with the number rather than living in docs. Same
// affordance as CashStatusLegend, which explains the six bill statuses.
export function MetricInfo({ text }: { text: string }) {
  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger
          aria-label={text}
          className="text-muted-foreground hover:text-foreground shrink-0 transition-colors"
        >
          <InfoIcon className="size-3.5" />
        </TooltipTrigger>
        <TooltipContent variant="surface" className="max-w-56">
          {text}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

// One figure. `size` only changes the number's type scale — label and fiat
// treatment stay identical so the lead pair and the four history tiles read
// as one grid rather than two unrelated blocks. `sensitive` opts the figure
// into the app's balance-hiding, `slashed-zero` (on the Card) keeps digits
// legible.
export function Stat({
  label,
  info,
  amountMloki,
  size = "sm",
  children,
}: {
  label: string;
  info: string;
  amountMloki: number;
  size?: "sm" | "lg";
  children?: React.ReactNode;
}) {
  return (
    <div className="min-w-0">
      <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
        <MetricInfo text={info} />
        {label}
      </p>
      <p
        className={cn(
          "font-semibold balance sensitive",
          size === "lg" ? "text-2xl" : "text-xl"
        )}
      >
        <FormattedFlokicoinAmount amount={amountMloki} />
      </p>
      <FormattedFiatAmount amount={Math.floor(amountMloki / 1000)} />
      {children}
    </div>
  );
}

// A glanceable health read for the coverage panel: can this hub honour the
// bills it has already issued? Outstanding is what it owes; shortfall is how
// much of that its bills do not currently hold. One coloured word is the first
// thing an operator should be able to read without doing arithmetic.
function CoverageBadge({
  outstandingMloki,
  shortfallMloki,
}: {
  outstandingMloki: number;
  shortfallMloki: number;
}) {
  const { t } = useTranslation("circles");
  const base =
    "inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium";
  if (outstandingMloki === 0) {
    return (
      <span className={cn(base, "bg-muted text-muted-foreground")}>
        {t("cashKpis.coverageNone")}
      </span>
    );
  }
  if (shortfallMloki > 0) {
    return (
      <span
        className={cn(
          base,
          "border border-warning-foreground/30 bg-warning-foreground/10 text-warning-foreground"
        )}
      >
        <TriangleAlertIcon className="size-3" />
        {t("cashKpis.coverageUnderfunded")}
      </span>
    );
  }
  return (
    <span
      className={cn(
        base,
        "border border-positive-foreground/30 bg-positive-foreground/10 text-positive-foreground"
      )}
    >
      <ShieldCheckIcon className="size-3" />
      {t("cashKpis.coverageCovered")}
    </span>
  );
}

// Everything an operator checks before they do anything else, in one card:
// what the hub holds (and can still issue against), whether it can honour what
// it already owes, and — once it has issued anything — the flow of value
// through it. A brand-new hub shows a calm empty state rather than a wall of
// zeroes.
//
// AppUsage is deliberately *not* reused here — its "Total Spent"/"Total
// Received" restate Issued/Redeemed for a Cash Hub, and it computes them by
// walking every transaction the hub ever had, a page at a time, purely to
// render two numbers this card already gets from /cash-stats in one request.
export function CashHubOverview({
  hub,
  stats,
}: {
  hub: App;
  stats: CashHubStats;
}) {
  const { t } = useTranslation("circles");
  const { t: ta } = useTranslation("apps");

  // A hub that has never minted a bill has no flow to show — four zero tiles
  // and a "nothing redeemed yet" line read as broken, not new.
  const hasActivity = stats.minted_mloki > 0;

  return (
    <Card className="slashed-zero">
      <CardContent className="grid gap-5">
        {/* Hero: what the hub holds, with the top-up / draw-down controls that
            change it, beside a coverage panel that answers "can it honour what
            it owes?" at a glance. */}
        <div className="flex flex-col gap-4 sm:flex-row sm:items-stretch sm:justify-between">
          <div className="min-w-0">
            <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
              <MetricInfo text={t("cashKpis.balanceInfo")} />
              {ta("usage.isolatedBalance")}
            </p>
            <p className="balance sensitive mt-0.5 text-3xl font-semibold">
              <FormattedFlokicoinAmount amount={hub.balance} />
            </p>
            <FormattedFiatAmount amount={Math.floor(hub.balance / 1000)} />
            <div className="mt-3 flex flex-wrap items-center gap-2">
              {hub.balance > 0 && (
                <IsolatedAppDrawDownDialog appId={hub.id}>
                  <Button size="sm" variant="outline">
                    <CircleMinusIcon />
                    {ta("usage.decrease")}
                  </Button>
                </IsolatedAppDrawDownDialog>
              )}
              <IsolatedAppTopupDialog appId={hub.id}>
                <Button size="sm" variant="outline">
                  <CirclePlusIcon />
                  {ta("usage.increase")}
                </Button>
              </IsolatedAppTopupDialog>
            </div>
          </div>

          {/* The coverage panel: what the hub owes, and whether its bills can
              cover it. Set apart with a tint + border so health reads as a
              distinct object, not a twin of the balance beside it. */}
          <div className="bg-muted/40 flex flex-col justify-between gap-2 rounded-lg border p-3 sm:min-w-[12rem] sm:text-right">
            <div>
              <p className="text-muted-foreground flex items-center gap-1.5 text-xs sm:justify-end">
                <MetricInfo text={t("cashKpis.outstandingInfo")} />
                {t("cashKpis.outstanding")}
              </p>
              <p className="balance sensitive mt-0.5 text-2xl font-semibold">
                <FormattedFlokicoinAmount amount={stats.outstanding_mloki} />
              </p>
              <p className="text-muted-foreground text-xs">
                {t("cashKpis.outstandingHint", {
                  count: stats.outstanding_count,
                })}
              </p>
            </div>
            <div className="sm:flex sm:justify-end">
              <CoverageBadge
                outstandingMloki={stats.outstanding_mloki}
                shortfallMloki={stats.shortfall_mloki}
              />
            </div>
          </div>
        </div>

        {/* Flow of value through the hub — only once there is any. */}
        {hasActivity ? (
          <>
            <div className="grid grid-cols-2 gap-4 border-t pt-4 sm:grid-cols-4">
              <Stat
                label={t("cashKpis.issued")}
                info={t("cashKpis.issuedInfo")}
                amountMloki={stats.minted_mloki}
              />
              <Stat
                label={t("cashKpis.redeemed")}
                info={t("cashKpis.redeemedInfo")}
                amountMloki={stats.redeemed_mloki}
              />
              <Stat
                label={t("cashKpis.returned")}
                info={t("cashKpis.returnedInfo")}
                amountMloki={stats.returned_mloki}
              />
              <Stat
                label={t("cashKpis.feesEarned")}
                info={t("cashKpis.feesEarnedInfo")}
                amountMloki={stats.fees_earned_mloki}
              />
            </div>

            <div className="text-muted-foreground flex flex-wrap gap-x-6 gap-y-1 text-xs">
              <span className="flex items-center gap-1.5">
                <MetricInfo text={t("cashKpis.medianTimeToRedeemInfo")} />
                {t("cashKpis.medianTimeToRedeem")}:{" "}
                <span className="text-foreground font-medium">
                  {stats.median_time_to_redeem_secs === null ? (
                    t("cashKpis.noRedemptionsYet")
                  ) : (
                    <MedianDuration secs={stats.median_time_to_redeem_secs} />
                  )}
                </span>
              </span>
              {/*
                Only shown when non-zero: a write-off means value that could
                not be returned to the hub at all, so it should stand out when
                it happens rather than sit at zero as permanent furniture.
              */}
              {stats.written_off_mloki > 0 && (
                <span className="text-destructive flex items-center gap-1.5">
                  <MetricInfo text={t("cashKpis.writtenOffInfo")} />
                  {t("cashKpis.writtenOff")}:{" "}
                  <FormattedFlokicoinAmount amount={stats.written_off_mloki} />
                </span>
              )}
            </div>
          </>
        ) : (
          <p className="text-muted-foreground border-t pt-4 text-sm">
            {t("cashKpis.noActivityYet")}
          </p>
        )}

        {/* A hub need not have a budget at all — the row appears only once
            one is set, the same condition AppUsage's Budget card used. */}
        {hub.maxAmount > 0 && (
          <div className="border-t pt-4">
            <div className="mb-2 flex flex-row justify-between gap-4">
              <div className="min-w-0">
                <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
                  <MetricInfo text={t("cashKpis.leftInBudgetInfo")} />
                  {ta("usage.leftInBudget")}
                </p>
                <p className="balance sensitive text-xl font-semibold">
                  <FormattedFlokicoinAmount
                    amount={(hub.maxAmount - hub.budgetUsage) * 1000}
                  />
                </p>
              </div>
              <div className="min-w-0 text-end">
                <p className="text-muted-foreground flex items-center justify-end gap-1.5 text-xs">
                  <MetricInfo text={t("cashKpis.budgetRenewalInfo")} />
                  {ta("usage.budgetRenewal")}
                </p>
                <p className="balance sensitive text-xl font-semibold">
                  <FormattedFlokicoinAmount amount={hub.maxAmount * 1000} />
                  {hub.budgetRenewal !== "never" && (
                    <> / {getBudgetRenewalLabel(hub.budgetRenewal)}</>
                  )}
                </p>
              </div>
            </div>
            <Progress
              className="h-2"
              value={100 - (hub.budgetUsage * 100) / hub.maxAmount}
            />
          </div>
        )}
      </CardContent>
    </Card>
  );
}
