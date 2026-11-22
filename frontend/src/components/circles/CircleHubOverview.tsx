import { Gem, Users } from "lucide-react";
import { useTranslation } from "react-i18next";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { App, CircleHubStats } from "src/types";

// The hero lead labels read as uppercase eyebrows with a small leading icon
// (matching the Cash Hub detail + the design), distinct from the sentence-case
// stat-strip labels below.
function Eyebrow({
  icon: Icon,
  children,
}: {
  icon: typeof Gem;
  children: React.ReactNode;
}) {
  return (
    <p className="text-muted-foreground flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide">
      <Icon className="size-3.5" />
      {children}
    </p>
  );
}

function Stat({
  label,
  amountMloki,
  size = "sm",
  children,
}: {
  label: string;
  amountMloki: number;
  size?: "sm" | "lg";
  children?: React.ReactNode;
}) {
  return (
    <div className="min-w-0">
      <p className="text-muted-foreground text-xs">{label}</p>
      <p
        className={`balance sensitive font-semibold ${
          size === "lg" ? "text-2xl" : "text-xl"
        }`}
      >
        <FormattedFlokicoinAmount amount={amountMloki} />
      </p>
      <FormattedFiatAmount amount={Math.floor(amountMloki / 1000)} />
      {children}
    </div>
  );
}

// What a circle host checks before anything else, in one card.
//
// The circle hub page previously opened with the generic AppUsage card, whose
// "Total Spent"/"Total Received" are computed by walking every transaction the
// hub ever had, a page at a time, and which says nothing about the circle
// itself — not how many members there are, not how much of the hub's money is
// sitting in their wallets, not what the host earned for carrying it.
export function CircleHubOverview({
  hub,
  stats,
  section = "full",
  discoverability,
}: {
  hub: App;
  stats: CircleHubStats;
  // On a phone the dashboard pins the balance/allocated lead ("hero") above the
  // tabs and moves the spent/received/fees/members grid ("details") into a
  // Summary tab. "full" (default) renders both, as on desktop. Mirrors
  // CashHubOverview's prop.
  section?: "full" | "hero" | "details";
  // The circle's discoverability summary (is it listed on the network + a link
  // to manage it). Rendered in the hero's right column under Members — the
  // design folds it into the hero rather than a separate strip. Omitted on the
  // phone "details"-only render so it isn't duplicated.
  discoverability?: React.ReactNode;
}) {
  const { t } = useTranslation("circles");
  const showHero = section !== "details";
  const showDetails = section !== "hero";

  // Allocation leads: the total the hub is holding for this circle is what's
  // free plus what members already hold. The meter and breakdown read off that
  // one figure so "held = allocated + free to allocate" is legible at a glance.
  const allocated = stats.allocated_mloki;
  const free = hub.balance;
  const held = free + allocated;
  const allocatedPct = held > 0 ? Math.round((allocated / held) * 100) : 0;

  const membersBlock = (
    <div className="min-w-0">
      <Eyebrow icon={Users}>{t("circleKpis.members")}</Eyebrow>
      <p className="text-2xl font-semibold">{stats.members_count}</p>
      <p className="text-muted-foreground mt-1 text-xs">
        {/* A follow-set circle has no eligible count this hub can give: its
            members come from a set on relays, which is not stored here. Saying
            so beats printing a stale number, and beats a 0 that reads as
            "nobody". */}
        {stats.eligible_count === null
          ? t("circleCharts.eligibleUnknown")
          : t("circleCharts.eligibleCount", { count: stats.eligible_count })}
      </p>
    </div>
  );

  return (
    // Card-light: borderless lead, same as the Cash Hub detail.
    // grid-cols-1 clamps the column — see CashHubDashboard.
    <div className="slashed-zero grid grid-cols-1 gap-4">
      {/* "Held for this circle" leads, with allocation (meter + breakdown) on
          the left and the member count on the right — the circle operator's
          core question answered before the tabs. */}
      {showHero && (
        // One bordered hero CARD: card-bg "Held" lead + tinted Members half.
        <div className="grid overflow-hidden rounded-xl border bg-card shadow-sm sm:grid-cols-[1.3fr_1fr]">
          <div className="min-w-0 p-5 sm:p-6">
            <Eyebrow icon={Gem}>{t("circleKpis.held")}</Eyebrow>
            <p className="balance sensitive text-2xl font-semibold">
              <FormattedFlokicoinAmount amount={held} />
            </p>
            <p className="text-muted-foreground mt-1 text-xs">
              <FormattedFlokicoinAmount amount={allocated} />{" "}
              {t("circleCharts.allocatedWord")} ·{" "}
              <FormattedFlokicoinAmount amount={free} />{" "}
              {t("circleCharts.freeToAllocate")}
            </p>
            {held > 0 && (
              <>
                <div className="bg-muted mt-3 h-2 w-full overflow-hidden rounded-full">
                  <div
                    className="bg-primary h-full rounded-full transition-all"
                    style={{ width: `${Math.max(0, Math.min(100, allocatedPct))}%` }}
                  />
                </div>
                <div className="text-muted-foreground mt-1 flex justify-between text-xs">
                  <span>
                    {t("circleCharts.allocationShare", { percent: allocatedPct })}{" "}
                    {t("circleCharts.acrossMembers", {
                      count: stats.members_count,
                    })}
                  </span>
                  <span>
                    <FormattedFlokicoinAmount amount={free} />{" "}
                    {t("circleCharts.freeWord")}
                  </span>
                </div>
              </>
            )}
          </div>
          {/* Tinted right half + hairline divider (border-t stacked on mobile,
              border-s on desktop). */}
          <div className="flex min-w-0 flex-col gap-4 border-t bg-muted/40 p-5 sm:border-s sm:border-t-0 sm:p-6">
            {membersBlock}
            {discoverability && (
              <div className="border-t pt-3">{discoverability}</div>
            )}
          </div>
        </div>
      )}

      {showDetails && (
        // Stat strip = its OWN bordered card with a vertical divider between
        // every cell (gap-px over a border-coloured bg draws the lines).
        <div className="grid grid-cols-2 gap-px overflow-hidden rounded-xl border bg-border sm:grid-cols-4">
          <div className="bg-card p-4">
            <Stat
              label={t("circleKpis.spent")}
              amountMloki={stats.spent_mloki}
            />
          </div>
          <div className="bg-card p-4">
            <Stat
              label={t("circleKpis.received")}
              amountMloki={stats.received_mloki}
            />
          </div>
          <div className="bg-card p-4">
            <Stat
              label={t("circleKpis.feesEarned")}
              amountMloki={stats.fees_earned_mloki}
            />
          </div>
          <div className="bg-card p-4">
            <Stat
              label={t("circleKpis.maxPerWallet")}
              amountMloki={hub.circlePerWalletMaxMloki ?? 0}
            />
          </div>
        </div>
      )}
    </div>
  );
}
