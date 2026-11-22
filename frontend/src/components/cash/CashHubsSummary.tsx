import { ArrowLeftRightIcon, BanknoteIcon, CirclePlusIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import {
  CoverageBadge,
  MedianDuration,
  Stat,
} from "src/components/cash/CashHubOverview";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { LinkButton } from "src/components/ui/custom/link-button";
import { useFiatRate } from "src/hooks/useFiatRate";
import { useInfo } from "src/hooks/useInfo";
import { cn } from "src/lib/utils";
import { CashHubStats } from "src/types";

// Uppercase eyebrow with a small leading icon — matches the Cash Hub detail
// hero and the design, distinct from the sentence-case stat-strip labels.
function Eyebrow({
  icon: Icon,
  children,
}: {
  icon: typeof BanknoteIcon;
  children: React.ReactNode;
}) {
  return (
    <p className="text-muted-foreground flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide">
      <Icon className="size-3.5" />
      {children}
    </p>
  );
}

// The same figures CashHubOverview shows for one hub, totalled across every
// hub on the node — the Cash Hubs list's answer to "what is my whole cash
// operation doing", which previously required opening each hub in turn and
// adding up by hand. It mirrors the Cash Hub detail hero: a backing lead with
// its actions, beside a distinct "in circulation" coverage panel, then a flow
// strip. Analytics lives on the list's toolbar, not inside this card.
export function CashHubsSummary({ stats }: { stats: CashHubStats }) {
  const { t } = useTranslation("circles");
  const { data: info } = useInfo();
  const { data: fiatRate, error: fiatRateError } = useFiatRate();
  const hasFiat = info?.currency !== "LOKI" && !fiatRateError && !!fiatRate;

  // Coverage meter: how much of what's in circulation the node's backing can
  // honour. Full and green when nothing is outstanding or it's fully reserved;
  // short (amber) by the shortfall otherwise.
  const covered = stats.shortfall_mloki <= 0;
  const coveragePct =
    stats.outstanding_mloki === 0
      ? 100
      : Math.max(
          3,
          Math.min(
            100,
            ((stats.outstanding_mloki - stats.shortfall_mloki) /
              stats.outstanding_mloki) *
              100
          )
        );

  return (
    <div className="slashed-zero grid gap-4">
      {/* Hero: one bordered CARD split into a card-bg backing lead + a TINTED
          right half (in-circulation + coverage), divided by a hairline. */}
      <div className="grid overflow-hidden rounded-xl border bg-card shadow-sm sm:grid-cols-[1.3fr_1fr]">
        <div className="min-w-0 p-5 sm:p-6">
          <Eyebrow icon={BanknoteIcon}>
            {t("cashKpis.backingAll")} ·{" "}
            {t("cashKpis.allHubsCountHint", { count: stats.hubs_count })}
          </Eyebrow>
          <p className="balance sensitive mt-1 text-2xl font-bold">
            <FormattedFlokicoinAmount amount={stats.balance_mloki} />
          </p>
          <p className="text-muted-foreground mt-1 text-xs">
            {hasFiat && (
              <>
                ≈{" "}
                <FormattedFiatAmount
                  amount={Math.floor(stats.balance_mloki / 1000)}
                />{" "}
              </>
            )}
            {t("cashKpis.reservedAcross")}
          </p>
          <div className="mt-3 flex flex-wrap gap-2">
            <LinkButton to="/wallet/receive" size="sm">
              <CirclePlusIcon />
              {t("cashKpis.addBacking")}
            </LinkButton>
            <LinkButton to="/wallet" variant="outline" size="sm">
              {t("cashKpis.viewNodeWallet")}
            </LinkButton>
          </div>
        </div>

        {/* Tinted right half — divided by the hairline (border-t when stacked
            on mobile, border-s on desktop). */}
        <div className="min-w-0 border-t bg-muted/40 p-5 sm:border-s sm:border-t-0 sm:p-6">
          <Eyebrow icon={ArrowLeftRightIcon}>
            {t("cashKpis.inCirculation")}
          </Eyebrow>
          <p className="balance sensitive mt-1 text-2xl font-bold">
            <FormattedFlokicoinAmount amount={stats.outstanding_mloki} />
          </p>
          <div className="bg-muted mt-2 h-2 w-full overflow-hidden rounded-full">
            <div
              className={cn(
                "h-full rounded-full transition-all",
                covered ? "bg-positive-foreground" : "bg-warning-foreground"
              )}
              style={{ width: `${coveragePct}%` }}
            />
          </div>
          <div className="mt-2 flex items-center justify-between gap-2">
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
        </div>
      </div>

      {/* Flow strip — its OWN bordered card, five cells divided by vertical
          hairlines (median redeem the last), matching the design. */}
      <div className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border sm:grid-cols-5">
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
        {/* Median spans both columns on mobile so the 5-cell strip doesn't
            leave a bare half-cell (the gap's bg-border) in the last row. */}
        <div className="bg-card col-span-2 min-w-0 p-4 sm:col-span-1">
          <p className="text-muted-foreground text-xs">
            {t("cashKpis.medianRedeem")}
          </p>
          <p className="text-xl font-semibold">
            {stats.median_time_to_redeem_secs === null ? (
              <span className="text-muted-foreground text-base font-normal">
                {t("cashKpis.noRedemptionsYet")}
              </span>
            ) : (
              <MedianDuration secs={stats.median_time_to_redeem_secs} />
            )}
          </p>
        </div>
      </div>
    </div>
  );
}
