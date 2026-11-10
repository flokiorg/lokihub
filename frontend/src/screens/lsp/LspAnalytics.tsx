import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { useTranslation } from "react-i18next";
import AppHeader from "src/components/AppHeader";
import { ChartCard, ChartPlot } from "src/components/charts/ChartCard";
import { axisProps, SERIES } from "src/components/charts/chartTheme";
import {
  Card,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Skeleton } from "src/components/ui/skeleton";
import { useLspAnalytics } from "src/hooks/useLsp";
import { LspUnavailable } from "src/screens/lsp/LspUnavailable";
import { formatLoki } from "src/screens/lsp/types";

export function LspAnalytics() {
  const { t } = useTranslation("lsp");
  const { data, error } = useLspAnalytics();

  return (
    <div className="space-y-4">
      <AppHeader
        title={t("analytics.title")}
        description={t("analytics.description")}
      />

      {error ? (
        <LspUnavailable error={error} />
      ) : !data ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Kpi
              label={t("analytics.kpi.totalRevenue")}
              value={formatLoki(data.total_revenue)}
            />
            <Kpi
              label={t("analytics.kpi.successRate")}
              value={`${Math.round(data.success_rate)}%`}
            />
            <Kpi
              label={t("analytics.kpi.avgOrderSize")}
              value={formatLoki(data.avg_order_size)}
            />
            <Kpi
              label={t("analytics.kpi.activePeers")}
              value={String(data.active_peers)}
            />
          </div>

          <div className="grid gap-4 lg:grid-cols-2">
            <ChartCard
              title={t("analytics.revenueChart.title")}
              description={t("analytics.revenueChart.description")}
            >
              <ChartPlot>
                <AreaChart
                  data={data.revenue_history}
                  margin={{ top: 4, right: 4, bottom: 0, left: 0 }}
                >
                  <defs>
                    <linearGradient
                      id="lspRevenueFill"
                      x1="0"
                      y1="0"
                      x2="0"
                      y2="1"
                    >
                      <stop
                        offset="0%"
                        stopColor={SERIES.covered}
                        stopOpacity={0.45}
                      />
                      <stop
                        offset="100%"
                        stopColor={SERIES.covered}
                        stopOpacity={0}
                      />
                    </linearGradient>
                  </defs>
                  <CartesianGrid
                    strokeDasharray="3 3"
                    stroke="var(--border)"
                    vertical={false}
                  />
                  <XAxis dataKey="day" {...axisProps} minTickGap={16} />
                  <YAxis {...axisProps} width={56} />
                  <Tooltip
                    formatter={(value) => formatLoki(Number(value))}
                    contentStyle={{
                      background: "var(--popover)",
                      border: "1px solid var(--border)",
                      borderRadius: 8,
                      fontSize: 12,
                    }}
                  />
                  <Area
                    type="monotone"
                    dataKey="revenue"
                    name={t("analytics.revenueChart.series")}
                    stroke={SERIES.covered}
                    fill="url(#lspRevenueFill)"
                    strokeWidth={2}
                    animationDuration={600}
                  />
                </AreaChart>
              </ChartPlot>
            </ChartCard>

            <ChartCard
              title={t("analytics.distributionChart.title")}
              description={t("analytics.distributionChart.description")}
            >
              <ChartPlot>
                <BarChart
                  data={data.order_distribution}
                  margin={{ top: 4, right: 4, bottom: 0, left: 0 }}
                >
                  <CartesianGrid
                    strokeDasharray="3 3"
                    stroke="var(--border)"
                    vertical={false}
                  />
                  <XAxis dataKey="name" {...axisProps} />
                  <YAxis {...axisProps} width={40} allowDecimals={false} />
                  <Tooltip
                    cursor={{ fill: "var(--muted)", opacity: 0.4 }}
                    contentStyle={{
                      background: "var(--popover)",
                      border: "1px solid var(--border)",
                      borderRadius: 8,
                      fontSize: 12,
                    }}
                  />
                  <Bar
                    dataKey="value"
                    radius={[3, 3, 0, 0]}
                    animationDuration={600}
                  >
                    {data.order_distribution.map((slice) => (
                      <Cell key={slice.name} fill={slice.color} />
                    ))}
                  </Bar>
                </BarChart>
              </ChartPlot>
            </ChartCard>
          </div>
        </>
      )}
    </div>
  );
}

function Kpi({ label, value }: { label: string; value: string }) {
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardDescription>{label}</CardDescription>
        <CardTitle className="text-2xl slashed-zero">{value}</CardTitle>
      </CardHeader>
    </Card>
  );
}
