import { AlertTriangleIcon, CheckIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { Card } from "src/components/ui/card";
import CircleProgress from "src/components/ui/custom/circle-progress";
import { Skeleton } from "src/components/ui/skeleton";
import {
  useLspAnalytics,
  useLspNodes,
  useLspPeers,
  useLspSummary,
} from "src/hooks/useLsp";
import { cn } from "src/lib/utils";
import { LspUnavailable } from "src/screens/lsp/LspUnavailable";
import { LspPeer, LspSummary } from "src/screens/lsp/types";
import { useFormatLoki } from "src/screens/lsp/useFormatLoki";
import { shortenMiddle } from "src/utils/nostr";

// The LSP Overview is a LAUNCHPAD, not a mirror: it answers "am I healthy, am I
// earning, is anything wrong?" at a glance and links DOWN to the Orders, Peers
// and Analytics pages (which carry the full detail) instead of restating them.
// The header + tab bar are provided by LspLayout, so this screen renders no
// AppHeader of its own.
export function LspOverview() {
  const { t } = useTranslation("lsp");
  const formatLoki = useFormatLoki();
  const { data: summary, error: summaryError } = useLspSummary();
  const { data: analytics } = useLspAnalytics();
  const { data: peers } = useLspPeers();
  const { data: nodesResp } = useLspNodes();

  if (summaryError) {
    return <LspUnavailable error={summaryError} />;
  }
  if (!summary) {
    return <OverviewSkeleton />;
  }

  const active = summary.active_orders ?? {};
  const failed = active.FAILED ?? 0;
  const activeOrders = (active.CREATED ?? 0) + (active.PAID ?? 0);
  const freePct =
    summary.liquidity_total > 0
      ? Math.round((summary.liquidity_free / summary.liquidity_total) * 100)
      : 0;

  return (
    <div className="space-y-4">
      <AttentionBanner
        failed={failed}
        liquidityAvailable={summary.liquidity_available}
      />

      {/* KPI strip — one card, four columns, the operator's first read. */}
      <Card className="slashed-zero overflow-hidden p-0">
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4">
          <div className="relative border-b p-4 sm:border-b-0 lg:border-r">
            <Lbl>{t("overview.kpi.sellableLiquidity")}</Lbl>
            <p className="mt-1 text-2xl font-bold">
              {summary.liquidity_available
                ? formatLoki(summary.liquidity_total)
                : "—"}
            </p>
            {summary.liquidity_available ? (
              <>
                <div className="mt-2 h-2 w-full overflow-hidden rounded-full bg-muted">
                  <div
                    className="h-full rounded-full bg-primary"
                    style={{ width: `${Math.max(0, Math.min(100, freePct))}%` }}
                  />
                </div>
                <p className="mt-1.5 text-xs text-muted-foreground">
                  {t("overview.kpi.freePctReserved", {
                    pct: freePct,
                    reserved: formatLoki(summary.liquidity_blocked),
                  })}
                </p>
              </>
            ) : (
              <p className="mt-1.5 text-xs text-muted-foreground">
                {t("overview.kpi.liquidityUnknown")}
              </p>
            )}
          </div>

          <div className="relative border-b p-4 sm:border-b-0 lg:border-r">
            <Lbl>{t("overview.kpi.revenue24h")}</Lbl>
            <p className="mt-1 text-2xl font-bold">
              {formatLoki(summary.revenue_24h)}
            </p>
            <p className="mt-1.5 text-xs text-muted-foreground">
              <span className="font-medium text-positive-foreground">
                {summary.revenue_trend}
              </span>{" "}
              {t("overview.kpi.vsPriorDay", { trend: "" }).trim()}
            </p>
          </div>

          <Link
            to="/lsp/orders"
            className="relative border-b p-4 transition-colors hover:bg-muted/40 sm:border-b-0 lg:border-r"
          >
            <Lbl>{t("overview.kpi.activeOrders")}</Lbl>
            <span className="absolute right-4 top-4 text-muted-foreground">
              →
            </span>
            <p className="mt-1 text-2xl font-bold">{activeOrders}</p>
            <p className="mt-1.5 text-xs text-muted-foreground">
              {t("overview.kpi.opensOrders")}
            </p>
          </Link>

          <Link
            to="/lsp/analytics"
            className="flex items-center gap-3 p-4 transition-colors hover:bg-muted/40"
          >
            <CircleProgress
              value={Math.round(analytics?.success_rate ?? 0)}
              className="h-14 w-14 text-sm"
            />
            <div>
              <Lbl>{t("overview.kpi.successRate")}</Lbl>
              <p className="mt-0.5 text-xs text-muted-foreground">
                {t("overview.kpi.viewAnalytics")}
              </p>
            </div>
          </Link>
        </div>
      </Card>

      {/* Node + Top peers. Both read off the one summary/peers payload and link
          to the Peers page for the full table. */}
      <div className="grid gap-4 lg:grid-cols-[1.4fr_1fr]">
        <NodeCard summary={summary} />
        <TopPeersCard peers={peers} />
      </div>

      <FleetCard nodes={nodesResp?.nodes} />
    </div>
  );
}

// Only a multi-node daemon shows a fleet list; per-node config lives on Config.
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
    <Card className="slashed-zero p-4 px-5">
      <h4 className="mb-3 text-sm font-semibold">
        {t("overview.fleet.title", { count: nodes.length })}
      </h4>
      {nodes.map((n) => (
        <KV
          key={n.node_id}
          k={n.name || t("overview.fleet.nodeFallback")}
        >
          <span className="font-mono text-xs">{shortenMiddle(n.node_id)}</span>
        </KV>
      ))}
    </Card>
  );
}

function Lbl({ children }: { children: React.ReactNode }) {
  return (
    <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
      {children}
    </p>
  );
}

function AttentionBanner({
  failed,
  liquidityAvailable,
}: {
  failed: number;
  liquidityAvailable: boolean;
}) {
  const { t } = useTranslation("lsp");
  const warn = failed > 0 || !liquidityAvailable;
  const message =
    failed > 0
      ? t("overview.attention.failed", { count: failed })
      : !liquidityAvailable
        ? t("overview.attention.liquidityUnknown")
        : t("overview.attention.healthy");
  return (
    <div
      className={cn(
        "flex items-center gap-3 rounded-xl border p-3 px-4 text-sm",
        warn
          ? "border-warning-foreground/30 bg-warning text-warning-foreground"
          : "border-positive-foreground/30 bg-positive text-positive-foreground"
      )}
    >
      {warn ? (
        <AlertTriangleIcon className="size-4 shrink-0" />
      ) : (
        <CheckIcon className="size-4 shrink-0" />
      )}
      <span>{message}</span>
      {failed > 0 && (
        <Link
          to="/lsp/orders"
          className="ms-auto rounded-md border px-3 py-1.5 text-xs font-semibold"
        >
          {t("overview.attention.reviewOrders")}
        </Link>
      )}
    </div>
  );
}

function NodeCard({ summary }: { summary: LspSummary }) {
  const { t } = useTranslation("lsp");
  const formatLoki = useFormatLoki();
  return (
    <Card className="slashed-zero p-4 px-5">
      <h4 className="mb-3 text-sm font-semibold">{t("overview.node.title")}</h4>
      <KV k={t("overview.node.pubkey")}>
        <span className="font-mono">{shortenMiddle(summary.node_pubkey)}</span>
      </KV>
      <KV k={t("overview.node.channels")}>
        <span className="flex flex-wrap justify-end gap-1.5">
          <ChannelPill
            tone="ok"
            text={t("overview.node.channelsActive", {
              count: summary.channels_active,
            })}
          />
          <ChannelPill
            tone="muted"
            text={t("overview.node.channelsPending", {
              count: summary.channels_pending,
            })}
          />
          <ChannelPill
            tone={summary.channels_inactive > 0 ? "warn" : "muted"}
            text={t("overview.node.channelsInactive", {
              count: summary.channels_inactive,
            })}
          />
        </span>
      </KV>
      <KV k={t("overview.node.inboundOutbound")}>
        {formatLoki(summary.liquidity_inbound)} /{" "}
        {formatLoki(summary.liquidity_outbound)}
      </KV>
      <KV k={t("overview.node.peers")}>{summary.peers_connected}</KV>
      <KV k={t("overview.node.chainTip")}>
        {t(
          summary.synced_to_chain
            ? "overview.node.chainTipValue"
            : "overview.node.chainTipUnsynced",
          { height: summary.sync_height }
        )}
      </KV>
    </Card>
  );
}

function TopPeersCard({ peers }: { peers: LspPeer[] | undefined }) {
  const { t } = useTranslation("lsp");
  const formatLoki = useFormatLoki();
  const top = [...(peers ?? [])]
    .sort((a, b) => b.in_loki + b.out_loki - (a.in_loki + a.out_loki))
    .slice(0, 5);
  return (
    <Card className="slashed-zero p-4 px-5">
      <h4 className="mb-3 flex items-center text-sm font-semibold">
        {t("overview.topPeers.title")}
        <Link
          to="/lsp/peers"
          className="ms-auto text-xs font-semibold text-primary"
        >
          {t("overview.topPeers.all")}
        </Link>
      </h4>
      {top.map((p) => (
        <KV
          key={p.pub_key}
          k={
            <span className="font-mono">
              {p.alias || shortenMiddle(p.pub_key)}
            </span>
          }
        >
          {formatLoki(p.in_loki + p.out_loki)}
        </KV>
      ))}
    </Card>
  );
}

function ChannelPill({
  tone,
  text,
}: {
  tone: "ok" | "muted" | "warn";
  text: string;
}) {
  return (
    <span
      className={cn(
        "rounded-full border px-2 py-0.5 text-xs font-medium",
        tone === "ok" &&
          "border-positive-foreground/30 bg-positive text-positive-foreground",
        tone === "warn" &&
          "border-warning-foreground/30 bg-warning text-warning-foreground",
        tone === "muted" && "border-border bg-muted text-muted-foreground"
      )}
    >
      {text}
    </span>
  );
}

function KV({ k, children }: { k: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-3 border-b border-border/60 py-1.5 text-sm last:border-b-0">
      <span className="text-muted-foreground">{k}</span>
      <span className="text-right font-medium">{children}</span>
    </div>
  );
}

function OverviewSkeleton() {
  return (
    <div className="space-y-4">
      <Skeleton className="h-12 w-full" />
      <Skeleton className="h-28 w-full" />
      <div className="grid gap-4 lg:grid-cols-[1.4fr_1fr]">
        <Skeleton className="h-52 w-full" />
        <Skeleton className="h-52 w-full" />
      </div>
    </div>
  );
}
