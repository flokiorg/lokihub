import {
  ArrowUpDownIcon,
  CircleMinusIcon,
  CirclePlusIcon,
  CoinsIcon,
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
import { Progress } from "src/components/ui/progress";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "src/components/ui/tooltip";
import { useFiatRate } from "src/hooks/useFiatRate";
import { useInfo } from "src/hooks/useInfo";
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
      {/* No ⓘ icon on the strip — the design's stat cells are a bare
          label+value; `info` stays as a hover title for the curious. */}
      <p className="text-muted-foreground text-xs" title={info}>
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
export function CoverageBadge({
  shortfallMloki,
}: {
  outstandingMloki: number;
  shortfallMloki: number;
}) {
  const { t } = useTranslation("circles");
  const base =
    "inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium";
  // Underfunded is the only alarm state. Everything else — nothing outstanding,
  // or outstanding but fully reserved — reads as a calm green "Covered", the
  // one-word verdict the design leads with.
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
      {t("cashKpis.covered")}
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
  section = "full",
}: {
  hub: App;
  stats: CashHubStats;
  // On a phone the dashboard splits this card in two: "hero" (balance, its
  // controls, and coverage) stays pinned above the tabs, while "details" (the
  // flow stats, median and budget) moves into a Summary tab so the other tabs
  // aren't buried under it. "full" (default) renders both, as on desktop.
  section?: "full" | "hero" | "details";
}) {
  const { t } = useTranslation("circles");
  const { t: ta } = useTranslation("apps");
  // Whether to show the "≈ $X" fiat fragment at all — on regtest/LOKI there is
  // no rate, so the fiat + its separator are omitted rather than left dangling.
  const { data: info } = useInfo();
  const { data: fiatRate, error: fiatRateError } = useFiatRate();
  const hasFiat =
    info?.currency !== "LOKI" && !fiatRateError && !!fiatRate;
  // Coverage: a covered hub (no shortfall) reads GREEN; a short hub reads amber.
  const covered = stats.shortfall_mloki <= 0;

  // A hub that has never minted a bill has no flow to show — four zero tiles
  // and a "nothing redeemed yet" line read as broken, not new.
  const hasActivity = stats.minted_mloki > 0;

  const showHero = section !== "details";
  const showDetails = section !== "hero";

  return (
    // Card-light: the overview is the page's lead, rendered borderless (the
    // detail separates it from the tabs with a hairline) rather than inside an
    // elevated box. Only the coverage panel keeps a quiet tint, since health
    // is the one thing here that reads apart from the rest.
    // grid-cols-1 (= minmax(0,1fr)) clamps the column so a wide child can't
    // widen the page — see CashHubDashboard.
    <div className="slashed-zero grid grid-cols-1 gap-5">
      {/* Hero: what the hub holds, with the top-up / draw-down controls that
            change it, beside a coverage panel that answers "can it honour what
            it owes?" at a glance. */}
      {showHero && (
        // One bordered hero CARD split into a card-bg left lead + a tinted
        // right half (divided by a hairline) — the design's hero framing.
        <div className="grid overflow-hidden rounded-xl border bg-card shadow-sm sm:grid-cols-[1.3fr_1fr]">
          <div className="min-w-0 p-5 sm:p-6">
            <p className="text-muted-foreground flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide">
              <ArrowUpDownIcon className="size-3.5" />
              {t("cashKpis.backing")}
            </p>
            <p className="balance sensitive mt-0.5 text-3xl font-semibold">
              <FormattedFlokicoinAmount amount={hub.balance} />
            </p>
            {/* ≈ fiat, then a one-line coverage verdict — "more than enough to
                cover every live bill" when the reserve covers what's in
                circulation, or the shortfall when it doesn't. */}
            <p className="text-muted-foreground mt-1 flex flex-wrap items-center gap-x-1.5 text-xs">
              {hasFiat && (
                <>
                  <FormattedFiatAmount
                    amount={Math.floor(hub.balance / 1000)}
                  />
                  <span aria-hidden>·</span>
                </>
              )}
              {stats.shortfall_mloki > 0 ? (
                <span className="text-warning-foreground">
                  <FormattedFlokicoinAmount amount={stats.shortfall_mloki} />{" "}
                  {t("cashKpis.coverShort")}
                </span>
              ) : (
                <span>{t("cashKpis.coverEnough")}</span>
              )}
            </p>
            <div className="mt-3 flex flex-wrap items-center gap-2">
              <IsolatedAppTopupDialog appId={hub.id}>
                <Button size="sm" variant="outline">
                  <CirclePlusIcon />
                  {t("cashKpis.addBacking")}
                </Button>
              </IsolatedAppTopupDialog>
              {hub.balance > 0 && (
                <IsolatedAppDrawDownDialog appId={hub.id}>
                  <Button size="sm" variant="outline">
                    <CircleMinusIcon />
                    {t("cashKpis.withdraw")}
                  </Button>
                </IsolatedAppDrawDownDialog>
              )}
            </div>
          </div>

          {/* The tinted right half: "Cash in circulation" + coverage, divided
              from the backing lead by the hairline (border-t on mobile when the
              grid stacks, border-s on desktop). */}
          <div className="min-w-0 border-t bg-muted/40 p-5 sm:border-s sm:border-t-0 sm:p-6">
            <p className="text-muted-foreground flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide">
              <CoinsIcon className="size-3.5" />
              {t("cashKpis.inCirculation")}
            </p>
            <p className="balance sensitive mt-0.5 text-2xl font-semibold">
              <FormattedFlokicoinAmount amount={stats.outstanding_mloki} />
            </p>
            {/* Coverage meter: GREEN when the reserve covers what's in
                circulation, amber only on a shortfall. */}
            <div className="bg-muted mt-2 h-2 w-full overflow-hidden rounded-full">
              <div
                className={cn(
                  "h-full rounded-full transition-all",
                  covered ? "bg-positive-foreground" : "bg-warning-foreground"
                )}
                style={{
                  width: `${
                    stats.outstanding_mloki === 0
                      ? 100
                      : Math.max(
                          0,
                          Math.min(
                            100,
                            ((stats.outstanding_mloki - stats.shortfall_mloki) /
                              stats.outstanding_mloki) *
                              100
                          )
                        )
                  }%`,
                }}
              />
            </div>
            <div className="mt-1 flex items-center justify-between gap-2">
              <span className="text-muted-foreground text-xs">
                {t("cashKpis.outstandingHint", {
                  count: stats.outstanding_count,
                })}
              </span>
              <CoverageBadge
                outstandingMloki={stats.outstanding_mloki}
                shortfallMloki={stats.shortfall_mloki}
              />
            </div>
            {/* Median time to redeem — the "how fast does value leave" read. */}
            <p className="text-muted-foreground mt-2 flex items-center gap-1.5 text-xs">
              {t("cashKpis.medianTimeToRedeem")}:{" "}
              <span className="text-foreground font-medium">
                {stats.median_time_to_redeem_secs === null ? (
                  t("cashKpis.noRedemptionsYet")
                ) : stats.median_time_to_redeem_secs === 0 ? (
                  t("cashKpis.medianInstant")
                ) : (
                  <MedianDuration secs={stats.median_time_to_redeem_secs} />
                )}
              </span>
            </p>
          </div>
        </div>
      )}

      {/* Flow of value through the hub — only once there is any. */}
      {showDetails && (
        <>
          {/* Flow strip — its OWN bordered card with a vertical divider between
              every cell (the gap-px over a border-coloured bg draws the lines),
              ALWAYS present (zeros for a new hub) per the design. */}
          <div className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border sm:grid-cols-4">
            <div className="bg-card p-4">
              <Stat
                label={t("cashKpis.issued")}
                info={t("cashKpis.issuedInfo")}
                amountMloki={stats.minted_mloki}
              />
            </div>
            <div className="bg-card p-4">
              <Stat
                label={t("cashKpis.redeemed")}
                info={t("cashKpis.redeemedInfo")}
                amountMloki={stats.redeemed_mloki}
              />
            </div>
            <div className="bg-card p-4">
              <Stat
                label={t("cashKpis.returned")}
                info={t("cashKpis.returnedInfo")}
                amountMloki={stats.returned_mloki}
              />
            </div>
            <div className="bg-card p-4">
              <Stat
                label={t("cashKpis.feesEarned")}
                info={t("cashKpis.feesEarnedInfo")}
                amountMloki={stats.fees_earned_mloki}
              />
            </div>
          </div>
          {hasActivity
            ? // A write-off (value that could not be returned at all) stands
              // out when it happens rather than sitting at zero as furniture.
              stats.written_off_mloki > 0 && (
                <p className="text-destructive flex flex-wrap items-center gap-1.5 text-xs">
                  <MetricInfo text={t("cashKpis.writtenOffInfo")} />
                  {t("cashKpis.writtenOff")}:{" "}
                  <FormattedFlokicoinAmount amount={stats.written_off_mloki} />
                </p>
              )
            : // New hub: the strip above shows zeros; one calm caption explains
              // why, instead of hiding the strip.
              (
                <p className="text-muted-foreground text-xs">
                  {t("cashKpis.noActivityCaption")}
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
        </>
      )}
    </div>
  );
}
