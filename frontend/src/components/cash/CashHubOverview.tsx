import { CircleMinusIcon, CirclePlusIcon, InfoIcon } from "lucide-react";
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

function MedianDuration({ secs }: { secs: number }) {
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
function MetricInfo({ text }: { text: string }) {
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
function Stat({
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

// Everything an operator checks before they do anything else, in one card.
//
// This replaces two stacked blocks that used to run down half the page: the
// old two-card KPI row, and AppUsage's three-to-four generic cards. AppUsage
// is deliberately *not* reused here — its "Total Spent"/"Total Received"
// restate Issued/Redeemed for a Cash Hub, and it computes them by walking
// every transaction the hub ever had, a page at a time, purely to render two
// numbers this card already gets from /cash-stats in one request.
export function CashHubOverview({
  hub,
  stats,
}: {
  hub: App;
  stats: CashHubStats;
}) {
  const { t } = useTranslation("circles");
  const { t: ta } = useTranslation("apps");

  return (
    <Card className="slashed-zero">
      <CardContent className="grid gap-4">
        {/* Balance and Outstanding lead, and are the only two figures at
            full size: one is what the hub can still issue against, the
            other is what it already owes. Everything below is history. */}
        <div className="grid gap-4 sm:grid-cols-2">
          <Stat
            label={ta("usage.isolatedBalance")}
            info={t("cashKpis.balanceInfo")}
            amountMloki={hub.balance}
            size="lg"
          >
            {/* The two controls that change this number sit with it rather
                than in a card of their own. */}
            <div className="mt-2 flex flex-wrap items-center gap-2">
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
          </Stat>
          <Stat
            label={t("cashKpis.outstanding")}
            info={t("cashKpis.outstandingInfo")}
            amountMloki={stats.outstanding_mloki}
            size="lg"
          >
            <p className="text-muted-foreground mt-1 text-xs">
              {t("cashKpis.outstandingHint", { count: stats.outstanding_count })}
            </p>
          </Stat>
        </div>

        <div className="grid grid-cols-2 gap-4 border-t pt-4 sm:grid-cols-4">
          <Stat
            label={t("cashKpis.issued")}
            info={t("cashKpis.issuedInfo")}
            amountMloki={stats.issued_mloki}
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
            Only shown when non-zero: a write-off means value that could not
            be returned to the hub at all, so it should stand out when it
            happens rather than sit at zero as permanent furniture.
          */}
          {stats.written_off_mloki > 0 && (
            <span className="text-destructive flex items-center gap-1.5">
              <MetricInfo text={t("cashKpis.writtenOffInfo")} />
              {t("cashKpis.writtenOff")}:{" "}
              <FormattedFlokicoinAmount amount={stats.written_off_mloki} />
            </span>
          )}
        </div>

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
