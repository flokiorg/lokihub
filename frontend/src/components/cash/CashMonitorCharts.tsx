import { useTranslation } from "react-i18next";
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  ReferenceLine,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { ChartCard, ChartPlot } from "src/components/charts/ChartCard";
import {
  SERIES,
  axisProps,
  shortDate,
  toLoki,
} from "src/components/charts/chartTheme";
import { RichTooltip } from "src/components/charts/RichTooltip";
import { TooltipRow } from "src/components/charts/chartTheme";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import {
  CashExpiryBucketKey,
  CashHubDailyPoint,
  CashHubStats,
} from "src/types";

// Expiry buckets run urgent to relaxed. Colour carries the urgency, so the
// shape of the problem is readable before any label is.
// How many hubs/members get their own bar before the tail is summed into a
// single "others". Eight keeps bars wide enough to read at 200px.
const BREAKDOWN_LIMIT = 8;

const BUCKET_COLOR: Record<CashExpiryBucketKey, string> = {
  "24h": SERIES.urgent,
  "7d": SERIES.owed,
  "30d": SERIES.info,
  later: SERIES.covered,
  never: SERIES.muted,
};

// CashCoverage answers the one question that can actually hurt an operator:
// is every issued bill actually funded?
//
// It used to compare the hub's own balance against what was outstanding, and
// that was wrong in both directions. Minting is not bookkeeping inside the
// hub — cashwallet.Commit does a real internal transfer — so backing money
// leaves the hub's ledger the instant a bill is minted and lives in that
// bill's own app row. A hub that has minted its whole balance is perfectly
// solvent with a hub balance of zero, and the old comparison reported that
// healthy end state as "Short — cannot cover every bill". Worse, because it
// only ever read aggregates, a large unminted balance could mask one bill
// whose own ledger had fallen below its unclaimed slices — false assurance on
// exactly the failure this card exists to catch.
//
// So it now compares backing (what the bills themselves hold) against
// outstanding (what those bills owe), which are the same pool of money, and
// reports shortfall computed per bill with only deficits summed. Unminted
// capacity is shown alongside as what it is: room to issue more, not cover.
export function CashCoverage({
  backingMloki,
  outstandingMloki,
  shortfallMloki,
  capacityMloki,
  className,
}: {
  backingMloki: number;
  outstandingMloki: number;
  shortfallMloki: number;
  // The hub's own balance: what is left to mint against. Complementary to
  // the pair above, never compared with them.
  capacityMloki: number;
  className?: string;
}) {
  const { t } = useTranslation("circles");

  const owed = outstandingMloki;
  const short = shortfallMloki > 0;
  // Nothing owed is fully funded, not a division by zero.
  const ratio = owed > 0 ? backingMloki / owed : null;
  const funded = owed > 0 ? Math.min(backingMloki / owed, 1) : 1;

  return (
    <ChartCard
      className={className}
      title={t("cashCharts.coverageTitle")}
      description={t("cashCharts.coverageHint")}
      action={
        <span
          className={
            short
              ? "text-destructive text-sm font-semibold"
              : "text-muted-foreground text-sm"
          }
        >
          {short
            ? t("cashCharts.coverageShort")
            : ratio === null
              ? t("cashCharts.coverageNothingOwed")
              : t("cashCharts.coverageRatio", { ratio: ratio.toFixed(1) })}
        </span>
      }
    >
      <div className="bg-muted h-3 w-full overflow-hidden rounded-full">
        <div
          className="h-full rounded-full transition-[width] duration-500"
          style={{
            width: `${funded * 100}%`,
            background: short ? SERIES.urgent : SERIES.covered,
          }}
        />
      </div>

      <div className="mt-3 grid grid-cols-3 gap-4">
        <div className="min-w-0">
          <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
            <span
              aria-hidden
              className="size-2 shrink-0 rounded-[2px]"
              style={{ background: short ? SERIES.urgent : SERIES.covered }}
            />
            {t("cashCharts.coverageBacking")}
          </p>
          <p className="balance sensitive font-semibold tabular-nums">
            <FormattedFlokicoinAmount amount={backingMloki} />
          </p>
        </div>
        <div className="min-w-0">
          <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
            <span
              aria-hidden
              className="size-2 shrink-0 rounded-[2px]"
              style={{ background: SERIES.owed }}
            />
            {t("cashCharts.coverageOwed")}
          </p>
          <p className="balance sensitive font-semibold tabular-nums">
            <FormattedFlokicoinAmount amount={owed} />
          </p>
        </div>
        <div className="min-w-0">
          <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
            <span
              aria-hidden
              className="size-2 shrink-0 rounded-[2px]"
              style={{ background: SERIES.muted }}
            />
            {t("cashCharts.coverageCapacity")}
          </p>
          <p className="balance sensitive font-semibold tabular-nums">
            <FormattedFlokicoinAmount amount={capacityMloki} />
          </p>
        </div>
      </div>

      {short && (
        <p className="text-destructive mt-3 text-xs">
          {t("cashCharts.coverageShortDetail")}
        </p>
      )}
    </ChartCard>
  );
}

// CashExpiryRunway shows how long the outstanding liability has left.
//
// Every bucket is money that is about to do one of two things without anyone
// touching it: come back as a reclaim, or be written off. That makes it the
// most forward-looking figure on the page — the rest of the overview is
// history.
export function CashExpiryRunway({
  stats,
  className,
}: {
  stats: CashHubStats;
  className?: string;
}) {
  const { t } = useTranslation("circles");

  const labels: Record<CashExpiryBucketKey, string> = {
    "24h": t("cashCharts.expiry24h"),
    "7d": t("cashCharts.expiry7d"),
    "30d": t("cashCharts.expiry30d"),
    later: t("cashCharts.expiryLater"),
    never: t("cashCharts.expiryNever"),
  };

  const data = stats.expiry_buckets.map((b) => ({
    key: b.key,
    label: labels[b.key],
    loki: toLoki(b.mloki),
    mloki: b.mloki,
    count: b.count,
    fill: BUCKET_COLOR[b.key],
  }));

  return (
    <ChartCard
      className={className}
      title={t("cashCharts.expiryTitle")}
      description={t("cashCharts.expiryHint")}
    >
      <ChartPlot>
        <BarChart
          data={data}
          layout="vertical"
          margin={{ top: 4, right: 8, bottom: 0, left: 0 }}
        >
          <CartesianGrid
            strokeDasharray="3 3"
            stroke="var(--border)"
            horizontal={false}
          />
          <XAxis type="number" {...axisProps} />
          <YAxis type="category" dataKey="label" width={70} {...axisProps} />
          <Tooltip
            cursor={{ fill: "var(--muted)", opacity: 0.4 }}
            content={(props) => {
              const point = props.payload?.[0]?.payload as
                | (typeof data)[number]
                | undefined;
              return (
                <RichTooltip
                  active={props.active}
                  label={point?.label}
                  total={stats.outstanding_mloki}
                  rows={
                    point
                      ? [
                          {
                            name: t("cashKpis.outstanding"),
                            color: point.fill,
                            mloki: point.mloki,
                            count: point.count,
                          },
                        ]
                      : []
                  }
                />
              );
            }}
          />
          <Bar dataKey="loki" radius={[0, 3, 3, 0]} animationDuration={600}>
            {data.map((d) => (
              <Cell key={d.key} fill={d.fill} />
            ))}
          </Bar>
        </BarChart>
      </ChartPlot>
    </ChartCard>
  );
}

// CashHubBreakdown answers "which hub is carrying this", which an aggregate
// curve cannot. On a list page that is usually the more useful question: the
// node-wide total can look calm while one hub drifts.
export function CashHubBreakdown({
  stats,
  className,
}: {
  stats: CashHubStats;
  className?: string;
}) {
  const { t } = useTranslation("circles");

  // Only the leading few get their own bar; the rest are summed into one.
  //
  // The rows arrive ordered largest-first, and a node can hold far more hubs
  // than a 200px plot has room for — past a handful the bars go hairline and
  // the axis labels overlap into an unreadable smear. The question this chart
  // answers is "which hub is carrying this", and that is answered by the
  // leaders; the tail only needs to be accounted for, not enumerated.
  const top = stats.per_hub.slice(0, BREAKDOWN_LIMIT);
  const rest = stats.per_hub.slice(BREAKDOWN_LIMIT);

  const data = top.map((h) => ({
    name: h.name,
    owedLoki: toLoki(h.outstanding_mloki),
    balanceLoki: toLoki(h.balance_mloki),
    owedMloki: h.outstanding_mloki,
    balanceMloki: h.balance_mloki,
    count: h.outstanding_count,
  }));
  if (rest.length) {
    const owedMloki = rest.reduce((n, h) => n + h.outstanding_mloki, 0);
    const balanceMloki = rest.reduce((n, h) => n + h.balance_mloki, 0);
    data.push({
      name: t("cashCharts.othersBar", { count: rest.length }),
      owedLoki: toLoki(owedMloki),
      balanceLoki: toLoki(balanceMloki),
      owedMloki,
      balanceMloki,
      count: rest.reduce((n, h) => n + h.outstanding_count, 0),
    });
  }

  return (
    <ChartCard
      className={className}
      title={t("cashCharts.perHubTitle")}
      description={t("cashCharts.perHubHint")}
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
          <YAxis {...axisProps} width={48} />
          <Tooltip
            cursor={{ fill: "var(--muted)", opacity: 0.4 }}
            content={(props) => {
              const point = props.payload?.[0]?.payload as
                | (typeof data)[number]
                | undefined;
              return (
                <RichTooltip
                  active={props.active}
                  label={point?.name}
                  rows={
                    point
                      ? [
                          {
                            name: t("cashCharts.coverageBalance"),
                            color: SERIES.covered,
                            mloki: point.balanceMloki,
                          },
                          {
                            name: t("cashKpis.outstanding"),
                            color: SERIES.owed,
                            mloki: point.owedMloki,
                            count: point.count,
                          },
                        ]
                      : []
                  }
                  footer={
                    point && point.owedMloki > point.balanceMloki
                      ? t("cashCharts.coverageShort")
                      : undefined
                  }
                />
              );
            }}
          />
          {/* Grouped, never stacked: a hub's balance and what it owes are two
            independent quantities being compared, not parts of one total.
            Stacking them would draw a bar whose height means nothing. */}
          <Bar
            dataKey="balanceLoki"
            name={t("cashCharts.coverageBalance")}
            fill={SERIES.covered}
            radius={[3, 3, 0, 0]}
            animationDuration={600}
          />
          <Bar
            dataKey="owedLoki"
            name={t("cashKpis.outstanding")}
            fill={SERIES.owed}
            radius={[3, 3, 0, 0]}
            animationDuration={600}
          />
        </BarChart>
      </ChartPlot>
    </ChartCard>
  );
}

// CashDailyFlow replaces the stacked bar chart that used to sit on a hub's
// dashboard.
//
// That one stacked issued, redeemed and returned into a single column, which
// is wrong twice over. They are flows in opposite directions, so the column's
// height was "money issued plus money that came back" — a quantity nobody
// needs. And stacking implied the day's redemptions came out of the same day's
// issuance, when a bill redeemed today was almost always issued days earlier.
// It also silently omitted splits and write-offs, so it never reconciled with
// the totals printed above it.
//
// Here issuance goes up and everything that leaves the outstanding pool goes
// down. Those four outflows ARE parts of one whole — every one of them is
// value ceasing to be redeemable — so stacking them together is honest, and
// the gap either side of the axis is the day's net change: exactly the delta
// that drives the outstanding curve.
export function CashDailyFlow({
  daily,
  className,
}: {
  daily: CashHubDailyPoint[];
  className?: string;
}) {
  const { t } = useTranslation("circles");

  const data = daily.map((d) => ({
    date: shortDate(d.date),
    issued: toLoki(d.issued_mloki),
    // Negative so they draw below the axis. The tooltip re-reads the raw
    // mloki fields, so the sign never reaches anything a person sees.
    redeemed: -toLoki(d.redeemed_mloki),
    returned: -toLoki(d.returned_mloki),
    split: -toLoki(d.split_mloki),
    writtenOff: -toLoki(d.written_off_mloki),
    raw: d,
  }));

  return (
    <ChartCard
      className={className}
      title={t("cashCharts.flowTitle")}
      description={t("cashCharts.flowHint")}
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
              const d = point.raw;
              const out =
                d.redeemed_mloki +
                d.returned_mloki +
                d.split_mloki +
                d.written_off_mloki;
              const net = d.issued_mloki - out;
              const rows: TooltipRow[] = [
                {
                  name: t("cashCharts.issuedSeries"),
                  color: SERIES.owed,
                  mloki: d.issued_mloki,
                },
                {
                  name: t("cashCharts.redeemedSeries"),
                  color: SERIES.covered,
                  mloki: d.redeemed_mloki,
                },
                {
                  name: t("cashCharts.returnedSeries"),
                  color: SERIES.info,
                  mloki: d.returned_mloki,
                },
                {
                  name: t("cashCharts.splitSeries"),
                  color: SERIES.muted,
                  mloki: d.split_mloki,
                },
              ];
              // A write-off is a loss rather than a recovery, so it appears
              // only on the days it actually happened instead of sitting at
              // zero and being read as routine.
              if (d.written_off_mloki > 0) {
                rows.push({
                  name: t("cashCharts.writtenOffSeries"),
                  color: SERIES.urgent,
                  mloki: d.written_off_mloki,
                });
              }
              return (
                <RichTooltip
                  active={props.active}
                  label={point.date}
                  rows={rows}
                  footer={t("cashCharts.netFooter", {
                    net: Math.round(net / 1000).toLocaleString(),
                  })}
                />
              );
            }}
          />
          <Bar
            dataKey="issued"
            stackId="in"
            fill={SERIES.owed}
            animationDuration={600}
          />
          <Bar
            dataKey="redeemed"
            stackId="out"
            fill={SERIES.covered}
            animationDuration={600}
          />
          <Bar
            dataKey="returned"
            stackId="out"
            fill={SERIES.info}
            animationDuration={600}
          />
          <Bar
            dataKey="split"
            stackId="out"
            fill={SERIES.muted}
            animationDuration={600}
          />
          <Bar
            dataKey="writtenOff"
            stackId="out"
            fill={SERIES.urgent}
            animationDuration={600}
          />
        </BarChart>
      </ChartPlot>
    </ChartCard>
  );
}

// CashOutstandingChart is the liability curve: how much is owed, and whether
// that is trending up.
//
// Anchored to today's real total and reconstructed BACKWARDS, rather than
// accumulated forwards from zero.
//
// Forwards was wrong for any hub older than the window: the series only covers
// the last cashStatsWindowDays, so a bill issued before it is missing from the
// running sum while still counting toward Outstanding — the curve started too
// low and its right-hand end never met the figure printed directly above it.
// It also went negative whenever a redemption inside the window belonged to
// issuance outside it, which a Math.max(…, 0) clamp hid rather than fixed.
//
// Walking back from the known total needs no extra data and is exact:
// outstanding(d-1) = outstanding(d) − delta(d). Pre-window issuance is
// recovered automatically, because a redemption inside the window pushes the
// reconstructed earlier value UP by exactly that amount.
export function CashOutstandingChart({
  daily,
  outstandingMloki,
  className,
}: {
  className?: string;
  daily: CashHubDailyPoint[];
  // Today's real outstanding total, the same figure the KPI tile shows.
  outstandingMloki: number;
}) {
  const { t } = useTranslation("circles");

  // One day's net change in outstanding value. Every way a slice leaves the
  // pool counts, not just the two obvious ones: a split moves value into
  // another bill (which is itself issued again, so that bill's own issued row
  // puts it back), and a write-off removes it for good.
  const dailyDelta = (d: CashHubDailyPoint) =>
    toLoki(
      d.issued_mloki -
        d.redeemed_mloki -
        d.returned_mloki -
        d.split_mloki -
        d.written_off_mloki
    );

  const outstandingByDay = new Array<number>(daily.length);
  let running = toLoki(outstandingMloki);
  for (let i = daily.length - 1; i >= 0; i--) {
    outstandingByDay[i] = running;
    running -= dailyDelta(daily[i]);
  }
  const data = daily.map((d, i) => ({
    date: shortDate(d.date),
    // A guard, not arithmetic: the walk above is exact when the series and
    // the total agree, and they are read from the same response.
    outstanding: Math.max(outstandingByDay[i], 0),
  }));

  return (
    <ChartCard
      className={className}
      title={t("cashCharts.outstandingTitle")}
      description={t("cashCharts.outstandingHint")}
    >
      <ChartPlot>
        <AreaChart
          data={data}
          margin={{ top: 4, right: 4, bottom: 0, left: 0 }}
        >
          <defs>
            <linearGradient
              id="cashOutstandingFill"
              x1="0"
              y1="0"
              x2="0"
              y2="1"
            >
              <stop offset="0%" stopColor={SERIES.owed} stopOpacity={0.45} />
              <stop offset="100%" stopColor={SERIES.owed} stopOpacity={0} />
            </linearGradient>
          </defs>
          <CartesianGrid
            strokeDasharray="3 3"
            stroke="var(--border)"
            vertical={false}
          />
          <XAxis dataKey="date" {...axisProps} minTickGap={24} />
          <YAxis {...axisProps} width={52} />
          <Tooltip
            cursor={{ stroke: "var(--border)" }}
            content={(props) => {
              const point = props.payload?.[0]?.payload as
                | (typeof data)[number]
                | undefined;
              return (
                <RichTooltip
                  active={props.active}
                  label={point?.date}
                  rows={
                    point
                      ? [
                          {
                            name: t("cashCharts.outstandingSeries"),
                            color: SERIES.owed,
                            mloki: point.outstanding * 1000,
                          },
                        ]
                      : []
                  }
                />
              );
            }}
          />
          <Area
            type="monotone"
            dataKey="outstanding"
            name={t("cashCharts.outstandingSeries")}
            stroke={SERIES.owed}
            fill="url(#cashOutstandingFill)"
            strokeWidth={2}
            animationDuration={600}
          />
        </AreaChart>
      </ChartPlot>
    </ChartCard>
  );
}
