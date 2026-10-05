import { useTranslation } from "react-i18next";
import FormattedFiatAmount from "src/components/FormattedFiatAmount";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { Card, CardContent } from "src/components/ui/card";
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
  analytics,
}: {
  hub: App;
  stats: CircleHubStats;
  // Same slot as CashHubOverview's analytics prop: rendered as the card's
  // last section instead of as a separate block elsewhere on the page.
  analytics?: React.ReactNode;
}) {
  const { t } = useTranslation("circles");

  return (
    <Card className="slashed-zero">
      <CardContent className="grid gap-4">
        {/* Balance and allocated lead: what the hub can still hand out, and
            what members are already holding. */}
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
              {t("circleCharts.acrossMembers", { count: stats.members_count })}
            </p>
          </Stat>
        </div>

        <div className="grid grid-cols-2 gap-4 border-t pt-4 sm:grid-cols-4">
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
              {/* A following-policy circle has no eligible count this hub can
                  give: its members come from the host's live contact list,
                  which is not stored here. Saying so beats printing a stale
                  number, and beats a 0 that would read as "nobody". */}
              {stats.eligible_count === null
                ? t("circleCharts.eligibleUnknown")
                : t("circleCharts.eligibleCount", {
                    count: stats.eligible_count,
                  })}
            </p>
          </div>
        </div>

        {analytics && <div className="border-t pt-4">{analytics}</div>}
      </CardContent>
    </Card>
  );
}
