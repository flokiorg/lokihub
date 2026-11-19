import { useTranslation } from "react-i18next";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { cn } from "src/lib/utils";
import { App, CircleHubStats } from "src/types";

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
}: {
  hub: App;
  stats: CircleHubStats;
  // On a phone the dashboard pins the balance/allocated lead ("hero") above the
  // tabs and moves the spent/received/fees/members grid ("details") into a
  // Summary tab. "full" (default) renders both, as on desktop. Mirrors
  // CashHubOverview's prop.
  section?: "full" | "hero" | "details";
}) {
  const { t } = useTranslation("circles");
  const showHero = section !== "details";
  const showDetails = section !== "hero";
  // As the first thing in a Summary tab the details grid shouldn't carry the
  // hairline that separates it from the hero on desktop.
  const detailsFirst = section === "details";

  return (
    // Card-light: borderless lead, same as the Cash Hub detail.
    // grid-cols-1 clamps the column — see CashHubDashboard.
    <div className="slashed-zero grid grid-cols-1 gap-4">
      {/* Balance and allocated lead: what the hub can still hand out, and
            what members are already holding. */}
      {showHero && (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Stat
              label={t("circleKpis.balance")}
              amountMloki={hub.balance}
              size="lg"
            />
            <Stat
              label={t("circleCharts.allocated")}
              amountMloki={stats.allocated_mloki}
              size="lg"
            >
              <p className="text-muted-foreground mt-1 text-xs">
                {t("circleCharts.acrossMembers", {
                  count: stats.members_count,
                })}
              </p>
            </Stat>
          </div>
          {/* Allocation meter — the same "state you can see" read as the Cash
              Hub's coverage bar: how much of the pool members are holding vs
              what's still free to allocate. */}
          {hub.balance + stats.allocated_mloki > 0 && (
            <div className="bg-muted h-2 w-full overflow-hidden rounded-full">
              <div
                className="bg-primary h-full rounded-full transition-all"
                style={{
                  width: `${Math.max(
                    0,
                    Math.min(
                      100,
                      (stats.allocated_mloki /
                        (hub.balance + stats.allocated_mloki)) *
                        100
                    )
                  )}%`,
                }}
              />
            </div>
          )}
        </>
      )}

      {showDetails && (
        <div
          className={cn(
            "grid grid-cols-2 gap-4 pt-4 sm:grid-cols-4",
            !detailsFirst && "border-t"
          )}
        >
          <Stat label={t("circleKpis.spent")} amountMloki={stats.spent_mloki} />
          <Stat
            label={t("circleKpis.received")}
            amountMloki={stats.received_mloki}
          />
          <Stat
            label={t("circleKpis.feesEarned")}
            amountMloki={stats.fees_earned_mloki}
          />
          <div className="min-w-0">
            <p className="text-muted-foreground text-xs">
              {t("circleKpis.members")}
            </p>
            <p className="text-xl font-semibold">{stats.members_count}</p>
            <p className="text-muted-foreground mt-1 text-xs">
              {/* A follow-set circle has no eligible count this hub can
                  give: its members come from a set on relays, which is not
                  stored here. Saying so beats printing a stale number, and
                  beats a 0 that would read as "nobody". */}
              {stats.eligible_count === null
                ? t("circleCharts.eligibleUnknown")
                : t("circleCharts.eligibleCount", {
                    count: stats.eligible_count,
                  })}
            </p>
          </div>
        </div>
      )}
    </div>
  );
}
