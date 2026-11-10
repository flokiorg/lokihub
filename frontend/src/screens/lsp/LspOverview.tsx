import { ActivityIcon, CoinsIcon, ServerIcon, UsersIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import AppHeader from "src/components/AppHeader";
import { LspServiceStatusCard } from "src/components/lsp/LspServiceStatusCard";
import { Badge } from "src/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import CircleProgress from "src/components/ui/custom/circle-progress";
import { Skeleton } from "src/components/ui/skeleton";
import {
  useLspAnalytics,
  useLspNodes,
  useLspPeers,
  useLspStatus,
  useLspSummary,
} from "src/hooks/useLsp";
import { LspUnavailable } from "src/screens/lsp/LspUnavailable";
import { formatLoki, orderStateVariant } from "src/screens/lsp/types";

export function LspOverview() {
  const { t } = useTranslation("lsp");
  const { data: status } = useLspStatus();
  const { data: summary, error: summaryError } = useLspSummary();
  const { data: analytics } = useLspAnalytics();
  const { data: nodesResp } = useLspNodes();
  const { data: peers } = useLspPeers();

  return (
    <div className="space-y-4">
      <AppHeader
        title={t("overview.title")}
        description={t("overview.description")}
      />

      {status && <LspServiceStatusCard status={status} />}

      {summaryError ? (
        <LspUnavailable error={summaryError} />
      ) : !summary ? (
        <KpiSkeleton />
      ) : (
        <>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <Kpi
              icon={<CoinsIcon className="size-4" />}
              label={t("overview.kpi.totalLiquidity")}
              value={
                summary.liquidity_available
                  ? formatLoki(summary.liquidity_total)
                  : "—"
              }
              sub={
                summary.liquidity_available
                  ? t("overview.kpi.free", {
                      amount: formatLoki(summary.liquidity_free),
                    })
                  : undefined
              }
            />
            <Kpi
              icon={<CoinsIcon className="size-4" />}
              label={t("overview.kpi.revenue24h")}
              value={formatLoki(summary.revenue_24h)}
              sub={t("overview.kpi.trend", { trend: summary.revenue_trend })}
            />
            <Kpi
              icon={<ActivityIcon className="size-4" />}
              label={t("overview.kpi.activeOrders")}
              value={String(
                (summary.active_orders?.CREATED ?? 0) +
                  (summary.active_orders?.PAID ?? 0)
              )}
              sub={t("overview.kpi.createdPaid")}
            />
            <SuccessKpi rate={analytics?.success_rate} />
          </div>

          <div className="grid gap-4 lg:grid-cols-2">
            <NodeCard summary={summary} />
            <OrderStateCard counts={summary.active_orders} />
          </div>
        </>
      )}

      <FleetCard nodes={nodesResp?.nodes} />
      <TopPeersCard peers={peers} />
    </div>
  );
}

function Kpi({
  icon,
  label,
  value,
  sub,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  sub?: string;
}) {
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardDescription className="flex items-center gap-2">
          {icon}
          {label}
        </CardDescription>
        <CardTitle className="text-2xl slashed-zero">{value}</CardTitle>
      </CardHeader>
      {sub && (
        <CardContent className="pt-0 text-xs text-muted-foreground">
          {sub}
        </CardContent>
      )}
    </Card>
  );
}

function SuccessKpi({ rate }: { rate: number | undefined }) {
  const { t } = useTranslation("lsp");
  const pct = Math.round(rate ?? 0);
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardDescription className="flex items-center gap-2">
          <ActivityIcon className="size-4" />
          {t("overview.kpi.successRate")}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex items-center gap-3 pt-0">
        <CircleProgress value={pct} className="h-16 w-16 text-sm" />
        <span className="text-xs text-muted-foreground">
          {t("overview.kpi.successFormula")}
        </span>
      </CardContent>
    </Card>
  );
}

function NodeCard({
  summary,
}: {
  summary: NonNullable<ReturnType<typeof useLspSummary>["data"]>;
}) {
  const { t } = useTranslation("lsp");
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base flex items-center gap-2">
          <ServerIcon className="size-4" />
          {summary.node_name || t("overview.node.fallbackName")}
          <Badge
            variant={
              summary.node_status === "online"
                ? "positive"
                : summary.node_status === "syncing"
                  ? "warning"
                  : "destructive"
            }
          >
            {summary.node_status}
          </Badge>
        </CardTitle>
        <CardDescription className="font-mono break-all">
          {summary.node_pubkey}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-3">
        <Stat
          label={t("overview.node.host")}
          value={summary.node_host || "—"}
        />
        <Stat
          label={t("overview.node.synced")}
          value={summary.synced_to_chain ? t("values.yes") : t("values.no")}
        />
        <Stat
          label={t("overview.node.height")}
          value={String(summary.sync_height)}
        />
        <Stat
          label={t("overview.node.peers")}
          value={String(summary.peers_connected)}
        />
        <Stat
          label={t("overview.node.channelsActive")}
          value={String(summary.channels_active)}
        />
        <Stat
          label={t("overview.node.channelsPending")}
          value={String(summary.channels_pending)}
        />
        <Stat
          label={t("overview.node.inbound")}
          value={formatLoki(summary.liquidity_inbound)}
        />
        <Stat
          label={t("overview.node.outbound")}
          value={formatLoki(summary.liquidity_outbound)}
        />
        <Stat
          label={t("overview.node.blocked")}
          value={formatLoki(summary.liquidity_blocked)}
        />
      </CardContent>
    </Card>
  );
}

function OrderStateCard({ counts }: { counts: Record<string, number> }) {
  const { t } = useTranslation("lsp");
  const states = ["CREATED", "PAID", "COMPLETED", "FAILED", "EXPIRED"];
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">
          {t("overview.orderStates.title")}
        </CardTitle>
        <CardDescription>
          {t("overview.orderStates.description")}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap gap-2">
        {states.map((s) => (
          <Badge key={s} variant={orderStateVariant(s)}>
            {s}: {counts?.[s] ?? 0}
          </Badge>
        ))}
      </CardContent>
    </Card>
  );
}

function FleetCard({
  nodes,
}: {
  nodes: { node_id: string; name: string }[] | undefined;
}) {
  const { t } = useTranslation("lsp");
  if (!nodes || nodes.length <= 1) {
    return null;
  }
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base flex items-center gap-2">
          <ServerIcon className="size-4" />
          {t("overview.fleet.title", { count: nodes.length })}
        </CardTitle>
        <CardDescription>{t("overview.fleet.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        {nodes.map((n) => (
          <div key={n.node_id} className="grid gap-0.5">
            <span className="font-medium">
              {n.name || t("overview.fleet.nodeFallback")}
            </span>
            <span className="font-mono text-xs text-muted-foreground break-all">
              {n.node_id}
            </span>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

function TopPeersCard({
  peers,
}: {
  peers:
    | { pub_key: string; alias: string; in_loki: number; out_loki: number }[]
    | undefined;
}) {
  const { t } = useTranslation("lsp");
  if (!peers || peers.length === 0) {
    return null;
  }
  const top = [...peers]
    .sort((a, b) => b.in_loki + b.out_loki - (a.in_loki + a.out_loki))
    .slice(0, 5);
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base flex items-center gap-2">
          <UsersIcon className="size-4" />
          {t("overview.topPeers.title")}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        {top.map((p) => (
          <div
            key={p.pub_key}
            className="flex items-center justify-between gap-3"
          >
            <span className="truncate">
              {p.alias || p.pub_key.slice(0, 16) + "…"}
            </span>
            <span className="shrink-0 text-muted-foreground slashed-zero">
              {formatLoki(p.in_loki + p.out_loki)}
            </span>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-0.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="slashed-zero">{value}</span>
    </div>
  );
}

function KpiSkeleton() {
  return (
    <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      {[0, 1, 2, 3].map((i) => (
        <Skeleton key={i} className="h-28 w-full" />
      ))}
    </div>
  );
}
