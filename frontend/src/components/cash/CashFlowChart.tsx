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
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
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
export function CashOutstandingChart({ daily }: { daily: CashHubDailyPoint[] }) {
  const { t } = useTranslation("circles");

  const dailyDelta = (d: CashHubDailyPoint) =>
    toLoki(d.issued_mloki - d.redeemed_mloki - d.returned_mloki);
  const data = daily.map((d, i) => ({
    date: shortDate(d.date),
    outstanding: Math.max(
      daily.slice(0, i + 1).reduce((sum, p) => sum + dailyDelta(p), 0),
      0
    ),
  }));

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-lg">
          {t("cashCharts.outstandingTitle")}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <ResponsiveContainer width="100%" height={200}>
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
        </ResponsiveContainer>
      </CardContent>
    </Card>
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
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-lg">{t("cashCharts.flowTitle")}</CardTitle>
      </CardHeader>
      <CardContent>
        <ResponsiveContainer width="100%" height={200}>
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
        </ResponsiveContainer>
      </CardContent>
    </Card>
  );
}
