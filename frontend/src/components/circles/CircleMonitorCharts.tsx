import { useTranslation } from "react-i18next";
import {
  Bar,
  BarChart,
  CartesianGrid,
  ReferenceLine,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { ChartCard, ChartPlot } from "src/components/charts/ChartCard";
import {
  SERIES,
  TooltipRow,
  axisProps,
  shortDate,
  toLoki,
} from "src/components/charts/chartTheme";
import { RichTooltip } from "src/components/charts/RichTooltip";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { App, CircleHubStats } from "src/types";

// How many members get their own bar before the tail is summed into one.
const MEMBER_BREAKDOWN_LIMIT = 8;

// CircleAllocation is the circle's answer to the cash hub's coverage bar.
//
// A circle hub is not at risk of insolvency the way a cash hub is — it can
// reclaim a member's wallet — so the useful reading is not "can I cover this"
// but "how much of what I hold is already committed". A host whose hub balance
// is nearly all allocated cannot admit another member without topping up, and
// nothing on the page said so before.
export function CircleAllocation({
  hub,
  stats,
  className,
}: {
  hub: App;
  stats: CircleHubStats;
  className?: string;
}) {
  const { t } = useTranslation("circles");

  const allocated = stats.allocated_mloki;
  // The hub's own balance is what is left to hand out; allocated is what
  // members already hold. Together they are everything the circle controls.
  const free = Math.max(hub.balance, 0);
  const total = allocated + free;
  const share = total > 0 ? allocated / total : 0;

  return (
    <ChartCard
      className={className}
      title={t("circleCharts.allocationTitle")}
      description={t("circleCharts.allocationHint")}
      action={
        <span className="text-muted-foreground text-sm">
          {t("circleCharts.allocationShare", {
            percent: Math.round(share * 100),
          })}
        </span>
      }
    >
      <div className="bg-muted h-3 w-full overflow-hidden rounded-full">
        <div
          className="h-full rounded-full transition-[width] duration-500"
          style={{ width: `${share * 100}%`, background: SERIES.owed }}
        />
      </div>

      <div className="mt-3 grid grid-cols-2 gap-4">
        <div className="min-w-0">
          <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
            <span
              aria-hidden
              className="size-2 shrink-0 rounded-[2px]"
              style={{ background: SERIES.owed }}
            />
            {t("circleCharts.allocated")}
          </p>
          <p className="balance sensitive font-semibold tabular-nums">
            <FormattedFlokicoinAmount amount={allocated} />
          </p>
          <p className="text-muted-foreground mt-1 text-xs">
            {t("circleCharts.acrossMembers", { count: stats.members_count })}
          </p>
        </div>
        <div className="min-w-0">
          <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
            <span
              aria-hidden
              className="size-2 shrink-0 rounded-[2px]"
              style={{ background: SERIES.covered }}
            />
            {t("circleCharts.unallocated")}
          </p>
          <p className="balance sensitive font-semibold tabular-nums">
            <FormattedFlokicoinAmount amount={free} />
          </p>
          <p className="text-muted-foreground mt-1 text-xs">
            {stats.eligible_count === null
              ? t("circleCharts.eligibleUnknown")
              : t("circleCharts.eligibleCount", {
                  count: stats.eligible_count,
                })}
          </p>
        </div>
      </div>
    </ChartCard>
  );
}

// CircleMemberBreakdown answers "who is actually using this circle".
//
// A member's balance and what they have spent are independent quantities being
// compared, so the bars are grouped, never stacked: a stacked column would
// have a height meaning "money held plus money already gone", which is not a
// thing.
export function CircleMemberBreakdown({
  stats,
  className,
}: {
  stats: CircleHubStats;
  className?: string;
}) {
  const { t } = useTranslation("circles");

  // Top few by their own bar, the tail summed into one — a circle can hold
  // far more members than a 200px plot has room for, and past a handful the
  // bars go hairline and the labels overlap. "Who is using this" is answered
  // by the leaders; the tail only needs accounting for.
  const top = stats.per_member.slice(0, MEMBER_BREAKDOWN_LIMIT);
  const rest = stats.per_member.slice(MEMBER_BREAKDOWN_LIMIT);

  const data = top.map((m) => ({
    name: m.name,
    balanceLoki: toLoki(m.balance_mloki),
    spentLoki: toLoki(m.spent_mloki),
    balanceMloki: m.balance_mloki,
    spentMloki: m.spent_mloki,
    capMloki: m.max_amount_mloki,
  }));
  if (rest.length) {
    const balanceMloki = rest.reduce((n, m) => n + m.balance_mloki, 0);
    const spentMloki = rest.reduce((n, m) => n + m.spent_mloki, 0);
    data.push({
      name: t("circleCharts.othersBar", { count: rest.length }),
      balanceLoki: toLoki(balanceMloki),
      spentLoki: toLoki(spentMloki),
      balanceMloki,
      spentMloki,
      // Caps do not add up into anything meaningful across members, so the
      // aggregated bar reports none rather than a sum nobody asked for.
      capMloki: 0,
    });
  }

  return (
    <ChartCard
      className={className}
      title={t("circleCharts.membersTitle")}
      description={t("circleCharts.membersHint")}
    >
      <ChartPlot>
        <BarChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: 0 }}>
          <CartesianGrid
            strokeDasharray="3 3"
            stroke="var(--border)"
            vertical={false}
          />
          {/* No interval={0}: forcing every label is what made this axis
              overlap. recharts drops labels it cannot fit instead. */}
          <XAxis dataKey="name" {...axisProps} />
          <YAxis {...axisProps} width={52} />
          <Tooltip
            cursor={{ fill: "var(--muted)", opacity: 0.4 }}
            content={(props) => {
              const point = props.payload?.[0]?.payload as
                | (typeof data)[number]
                | undefined;
              if (!point) {
                return null;
              }
              const rows: TooltipRow[] = [
                {
                  name: t("circleCharts.holds"),
                  color: SERIES.covered,
                  mloki: point.balanceMloki,
                },
                {
                  name: t("circleCharts.spent"),
                  color: SERIES.owed,
                  mloki: point.spentMloki,
                },
              ];
              return (
                <RichTooltip
                  active={props.active}
                  label={point.name}
                  rows={rows}
                  // A cap of 0 means uncapped, not "a budget of nothing", so
                  // the footer is omitted rather than claiming 0 remaining.
                  footer={
                    point.capMloki > 0
                      ? t("circleCharts.capFooter", {
                          cap: Math.round(
                            point.capMloki / 1000
                          ).toLocaleString(),
                        })
                      : t("circleCharts.uncapped")
                  }
                />
              );
            }}
          />
          <Bar
            dataKey="balanceLoki"
            name={t("circleCharts.holds")}
            fill={SERIES.covered}
            radius={[3, 3, 0, 0]}
            animationDuration={600}
          />
          <Bar
            dataKey="spentLoki"
            name={t("circleCharts.spent")}
            fill={SERIES.owed}
            radius={[3, 3, 0, 0]}
            animationDuration={600}
          />
        </BarChart>
      </ChartPlot>
    </ChartCard>
  );
}

// CircleDailyFlow shows member spending against what came in.
//
// Diverging rather than stacked, for the same reason the cash flow chart is:
// spending and receipts run in opposite directions, so adding them into one
// column would produce a height nobody needs. The gap either side of the axis
// is the day's net movement.
export function CircleDailyFlow({
  stats,
  className,
}: {
  stats: CircleHubStats;
  className?: string;
}) {
  const { t } = useTranslation("circles");

  const data = stats.daily.map((d) => ({
    date: shortDate(d.date),
    received: toLoki(d.received_mloki),
    // Negative so it draws below the axis; the tooltip reads the raw field,
    // so the sign never reaches anything a person sees.
    spent: -toLoki(d.spent_mloki),
    raw: d,
  }));

  return (
    <ChartCard
      className={className}
      title={t("circleCharts.flowTitle")}
      description={t("circleCharts.flowHint")}
    >
      <ChartPlot>
        <BarChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: 0 }}>
          <CartesianGrid
            strokeDasharray="3 3"
            stroke="var(--border)"
            vertical={false}
          />
          <XAxis dataKey="date" {...axisProps} minTickGap={24} />
          <YAxis {...axisProps} width={52} />
          <ReferenceLine y={0} stroke="var(--border)" />
          <Tooltip
            cursor={{ fill: "var(--muted)", opacity: 0.4 }}
            content={(props) => {
              const point = props.payload?.[0]?.payload as
                | (typeof data)[number]
                | undefined;
              if (!point) {
                return null;
              }
              const net = point.raw.received_mloki - point.raw.spent_mloki;
              return (
                <RichTooltip
                  active={props.active}
                  label={point.date}
                  rows={[
                    {
                      name: t("circleCharts.received"),
                      color: SERIES.covered,
                      mloki: point.raw.received_mloki,
                    },
                    {
                      name: t("circleCharts.spent"),
                      color: SERIES.owed,
                      mloki: point.raw.spent_mloki,
                    },
                  ]}
                  footer={t("cashCharts.netFooter", {
                    net: Math.round(net / 1000).toLocaleString(),
                  })}
                />
              );
            }}
          />
          <Bar
            dataKey="received"
            fill={SERIES.covered}
            animationDuration={600}
          />
          <Bar dataKey="spent" fill={SERIES.owed} animationDuration={600} />
        </BarChart>
      </ChartPlot>
    </ChartCard>
  );
}
