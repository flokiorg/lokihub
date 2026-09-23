import React from "react";
import { useTranslation } from "react-i18next";
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { CashHubDailyPoint } from "src/types";

// recharts was already a pinned dependency and entirely unused; these are the
// first charts in the app. They read their colours from the existing
// --chart-1..5 theme tokens (themes/*.css), which until now only fed avatar
// gradients — so the charts change with the selected theme instead of
// hard-coding hex values that would break in dark mode.
const COLORS = {
  issued: "var(--chart-1)",
  redeemed: "var(--chart-2)",
  returned: "var(--chart-3)",
};

// Charts are drawn in loki, not mloki: an axis labelled in thousandths is
// unreadable, and the KPI tiles above already carry exact figures.
const toLoki = (mloki: number) => Math.floor(mloki / 1000);

function shortDate(date: string) {
  // "2026-09-23" -> "09-23". The year is the same across a 30-day window, so
  // it is noise on an axis.
  return date.slice(5);
}

const axisProps = {
  stroke: "var(--muted-foreground)",
  fontSize: 11,
  tickLine: false,
  axisLine: false,
};

// Both charts render bare — a heading and a plot, no Card of their own.
// They live inside the dashboard's collapsible "Analytics" card, and a card
// nested in a card just draws a second border around the same content.
function ChartFrame({
  title,
  children,
}: {
  title: string;
  children: React.ReactElement;
}) {
  return (
    <div className="min-w-0">
      <p className="mb-2 text-sm font-medium">{title}</p>
      <ResponsiveContainer width="100%" height={200}>
        {children}
      </ResponsiveContainer>
    </div>
  );
}

function ChartTooltip() {
  return (
    <Tooltip
      contentStyle={{
        background: "var(--popover)",
        border: "1px solid var(--border)",
        borderRadius: "var(--radius)",
        color: "var(--popover-foreground)",
        fontSize: 12,
      }}
    />
  );
}

// CashOutstandingChart shows the hub's liability over time, reconstructed by
// walking the daily flow: everything issued, less everything that has since
// been redeemed or returned. It is cumulative on purpose — the question an
// operator asks is "how much do I owe right now, and is it trending up?", not
// "what happened on the 14th".
export function CashOutstandingChart({
  daily,
  outstandingMloki,
}: {
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

  // Anchored to today's real total and reconstructed BACKWARDS, rather than
  // accumulated forwards from zero.
  //
  // Forwards was wrong for any hub older than the window: the series only
  // covers the last cashStatsWindowDays, so a bill issued before it is
  // missing from the running sum while still counting toward Outstanding —
  // the curve started too low and its right-hand end never met the figure
  // printed directly above it. It also went negative whenever a redemption
  // inside the window belonged to issuance outside it, which the old
  // Math.max(…, 0) clamp hid rather than fixed.
  //
  // Walking back from the known total needs no extra data and is exact:
  // outstanding(d-1) = outstanding(d) − delta(d). Pre-window issuance is
  // recovered automatically, because a redemption inside the window pushes
  // the reconstructed earlier value UP by exactly that amount.
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
    <ChartFrame title={t("cashCharts.outstandingTitle")}>
      <AreaChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: 0 }}>
      <defs>
        <linearGradient id="cashOutstandingFill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={COLORS.issued} stopOpacity={0.5} />
          <stop offset="100%" stopColor={COLORS.issued} stopOpacity={0} />
        </linearGradient>
      </defs>
      <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" vertical={false} />
      <XAxis dataKey="date" {...axisProps} minTickGap={24} />
      <YAxis {...axisProps} width={48} />
      <ChartTooltip />
      <Area
        type="monotone"
        dataKey="outstanding"
        name={t("cashCharts.outstandingSeries")}
        stroke={COLORS.issued}
        fill="url(#cashOutstandingFill)"
        strokeWidth={2}
      />
      </AreaChart>
    </ChartFrame>
  );
}

// CashFlowChart shows what moved each day. Stacked because the three series
// are parts of one day's activity rather than independent lines.
export function CashFlowChart({ daily }: { daily: CashHubDailyPoint[] }) {
  const { t } = useTranslation("circles");

  const data = daily.map((d) => ({
    date: shortDate(d.date),
    issued: toLoki(d.issued_mloki),
    redeemed: toLoki(d.redeemed_mloki),
    returned: toLoki(d.returned_mloki),
  }));

  return (
    <ChartFrame title={t("cashCharts.flowTitle")}>
      <BarChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: 0 }}>
      <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" vertical={false} />
      <XAxis dataKey="date" {...axisProps} minTickGap={24} />
      <YAxis {...axisProps} width={48} />
      <ChartTooltip />
      <Bar
        dataKey="issued"
        name={t("cashCharts.issuedSeries")}
        stackId="flow"
        fill={COLORS.issued}
      />
      <Bar
        dataKey="redeemed"
        name={t("cashCharts.redeemedSeries")}
        stackId="flow"
        fill={COLORS.redeemed}
      />
      <Bar
        dataKey="returned"
        name={t("cashCharts.returnedSeries")}
        stackId="flow"
        fill={COLORS.returned}
      />
      </BarChart>
    </ChartFrame>
  );
}
