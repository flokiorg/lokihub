import {
  ArrowLeftIcon,
  CheckCircle2Icon,
  CopyIcon,
  SearchXIcon,
  XCircleIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link, useParams } from "react-router";
import QRCode from "src/components/QRCode";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { ExternalLinkButton } from "src/components/ui/custom/external-link-button";
import { Skeleton } from "src/components/ui/skeleton";
import { useInfo } from "src/hooks/useInfo";
import { useLspOrderDetail } from "src/hooks/useLsp";
import { copyToClipboard } from "src/lib/clipboard";
import { LspUnavailable } from "src/screens/lsp/LspUnavailable";
import {
  LspOrder,
  LspOrderTimelineEvent,
  orderStateVariant,
} from "src/screens/lsp/types";
import { useFormatLoki } from "src/screens/lsp/useFormatLoki";

// A copyable identifier — order inspection exists so the operator can take the
// id/txid/pubkey/invoice elsewhere (explorer, lncli, a ticket), so every mono
// value here is one click to the clipboard.
function CopyMono({ value }: { value: string }) {
  const { t } = useTranslation("lsp");
  return (
    <button
      type="button"
      onClick={() => copyToClipboard(value)}
      aria-label={t("orderDetail.copy")}
      className="group inline-flex max-w-full items-start gap-1.5 text-left"
    >
      <span className="break-all font-mono text-xs">{value}</span>
      <CopyIcon className="mt-0.5 size-3 shrink-0 text-muted-foreground transition-colors group-hover:text-foreground" />
    </button>
  );
}

export function LspOrderDetail() {
  const { t } = useTranslation("lsp");
  const { orderId } = useParams<{ orderId: string }>();
  const { data: detail, error } = useLspOrderDetail(orderId);
  const { data: info } = useInfo();

  // A bad/pruned order id is a 404, not a degraded LSP service — show a plain
  // "not found" instead of the generic "could not load LSP data" warning.
  const notFound = (error as { status?: number } | undefined)?.status === 404;

  // A drill-down below the shared LspLayout header + tabs: its own compact
  // sub-header (which order + a way back), not a second full page header.
  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-lg font-semibold">{t("orderDetail.title")}</h2>
          {orderId && (
            <div className="text-muted-foreground">
              <CopyMono value={orderId} />
            </div>
          )}
        </div>
        <Button variant="outline" size="sm" asChild>
          <Link to="/lsp/orders">
            <ArrowLeftIcon className="size-4" /> {t("orderDetail.back")}
          </Link>
        </Button>
      </div>

      {notFound ? (
        <div className="flex flex-col items-center gap-3 py-16 text-center">
          <SearchXIcon className="size-10 text-muted-foreground" />
          <p className="text-lg font-semibold">
            {t("orderDetail.notFound.title")}
          </p>
          <p className="max-w-sm text-sm text-muted-foreground">
            {t("orderDetail.notFound.body")}
          </p>
          <Button variant="secondary" size="sm" asChild>
            <Link to="/lsp/orders">{t("orderDetail.back")}</Link>
          </Button>
        </div>
      ) : error ? (
        <LspUnavailable error={error} />
      ) : !detail ? (
        <Skeleton className="h-64 w-full" />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={orderStateVariant(detail.order.state)}>
              {detail.order.state}
            </Badge>
            <Badge variant="outline">
              {t("orderDetail.paymentBadge", {
                state: detail.order.payment_state,
              })}
            </Badge>
            {detail.order.announce_channel && (
              <Badge variant="secondary">{t("orderDetail.announced")}</Badge>
            )}
          </div>

          <div className="grid gap-4 lg:grid-cols-2">
            <TimelineCard
              events={detail.timeline}
              state={detail.order.state}
              paymentState={detail.order.payment_state}
            />
            <PaymentCard invoice={detail.order.payment_invoice} />
          </div>

          <ChannelCard
            fundingTxid={detail.order.funding_txid}
            fundingOutpoint={detail.order.funding_outpoint}
            refundAddress={detail.order.refund_onchain_address}
            mempoolUrl={info?.mempoolUrl}
          />

          <ParamsCard order={detail.order} mempoolUrl={info?.mempoolUrl} />

          <p className="text-xs text-muted-foreground">
            {t("orderDetail.readOnlyNote")}
          </p>
        </>
      )}
    </div>
  );
}

function TimelineCard({
  events,
  state,
  paymentState,
}: {
  events: LspOrderTimelineEvent[];
  state: string;
  paymentState: string;
}) {
  const { t } = useTranslation("lsp");
  // lspd records only the steps that happened — a failed order's timeline stops
  // at ORDER_CREATED and never says it failed. Append an explicit failure row
  // (with a red marker, not a green check) so inspecting a FAILED/EXPIRED order
  // actually shows the failure instead of a lone checkmark.
  const failed = state === "FAILED" || state === "EXPIRED";
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">
          {t("orderDetail.timeline.title")}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {events.map((e, i) => (
          <div key={i} className="flex items-start gap-3">
            <CheckCircle2Icon className="mt-0.5 size-4 text-primary" />
            <div className="grid gap-0.5">
              <span className="text-sm font-medium">{e.event}</span>
              <span className="text-xs text-muted-foreground">{e.detail}</span>
              {e.timestamp && (
                <span className="text-xs text-muted-foreground slashed-zero">
                  {new Date(e.timestamp).toLocaleString()}
                </span>
              )}
            </div>
          </div>
        ))}
        {failed && (
          <div className="flex items-start gap-3">
            <XCircleIcon className="mt-0.5 size-4 text-destructive" />
            <div className="grid gap-0.5">
              <span className="text-sm font-medium text-destructive">
                {t(
                  state === "EXPIRED"
                    ? "orderDetail.timeline.expired"
                    : "orderDetail.timeline.failed"
                )}
              </span>
              <span className="text-xs text-muted-foreground">
                {t("orderDetail.timeline.failedHint", { state: paymentState })}
              </span>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function PaymentCard({ invoice }: { invoice: string }) {
  const { t } = useTranslation("lsp");
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">
          {t("orderDetail.payment.title")}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {invoice ? (
          <>
            <QRCode value={invoice} size={160} withIcon={false} />
            <CopyMono value={invoice} />
          </>
        ) : (
          <p className="text-sm text-muted-foreground">
            {t("orderDetail.payment.noInvoice")}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

function ChannelCard({
  fundingTxid,
  fundingOutpoint,
  refundAddress,
  mempoolUrl,
}: {
  fundingTxid: string | null;
  fundingOutpoint: number | null;
  refundAddress: string;
  mempoolUrl: string | undefined;
}) {
  const { t } = useTranslation("lsp");
  if (!fundingTxid && !refundAddress) {
    return null;
  }
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">
          {t("orderDetail.channel.title")}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        {fundingTxid && (
          <div className="grid gap-1">
            <span className="text-muted-foreground">
              {t("orderDetail.channel.fundingOutpoint")}{" "}
              {fundingOutpoint !== null
                ? t("orderDetail.channel.fundingIndex", {
                    index: fundingOutpoint,
                  })
                : ""}
            </span>
            <div className="flex flex-wrap items-center gap-2">
              <CopyMono value={fundingTxid} />
              {mempoolUrl && (
                <ExternalLinkButton
                  to={`${mempoolUrl}/tx/${fundingTxid}${
                    fundingOutpoint !== null
                      ? `#flow=&vout=${fundingOutpoint}`
                      : ""
                  }`}
                  variant="link"
                  className="h-auto p-0 text-xs"
                >
                  {t("orderDetail.channel.explorer")}
                </ExternalLinkButton>
              )}
            </div>
          </div>
        )}
        {refundAddress && (
          <div className="grid gap-1">
            <span className="text-muted-foreground">
              {t("orderDetail.channel.refundAddress")}
            </span>
            <CopyMono value={refundAddress} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function ParamsCard({
  order,
  mempoolUrl,
}: {
  order: LspOrder;
  mempoolUrl: string | undefined;
}) {
  const { t } = useTranslation("lsp");
  const formatLoki = useFormatLoki();
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">
          {t("orderDetail.params.title")}
        </CardTitle>
      </CardHeader>
      <CardContent className="grid grid-cols-2 gap-x-6 gap-y-3 text-sm sm:grid-cols-3">
        <Row
          label={t("orderDetail.params.lspBalance")}
          value={formatLoki(order.lsp_balance_loki)}
        />
        <Row
          label={t("orderDetail.params.clientBalance")}
          value={formatLoki(order.client_balance_loki)}
        />
        <Row
          label={t("orderDetail.params.orderTotal")}
          value={formatLoki(order.amount)}
        />
        <Row
          label={t("orderDetail.params.fee")}
          value={formatLoki(order.fee)}
        />
        <Row
          label={t("orderDetail.params.reserved")}
          value={formatLoki(order.reserved_amount_loki)}
        />
        <Row
          label={t("orderDetail.params.channelExpiry")}
          value={t("orderDetail.params.blocks", {
            count: order.channel_expiry_blocks,
          })}
        />
        <Row
          label={t("orderDetail.params.confirmations")}
          value={String(order.required_channel_confirmations)}
        />
        {/* Peer: the alias + colour the list shows, then the copyable pubkey and
            a jump to the node on the explorer — an actionable entity, not a
            dead string. */}
        <div className="grid gap-0.5 sm:col-span-2">
          <span className="text-xs text-muted-foreground">
            {t("orderDetail.params.peer")}
          </span>
          {order.peer_alias && (
            <span className="flex items-center gap-1.5 text-sm font-medium">
              {order.peer_color && (
                <i
                  className="size-2.5 shrink-0 rounded-full"
                  style={{ background: `#${order.peer_color}` }}
                />
              )}
              {order.peer_alias}
            </span>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <CopyMono value={order.peer_id} />
            {mempoolUrl && (
              <ExternalLinkButton
                to={`${mempoolUrl}/lightning/node/${order.peer_id}`}
                variant="link"
                className="h-auto p-0 text-xs"
              >
                {t("orderDetail.viewNode")}
              </ExternalLinkButton>
            )}
          </div>
        </div>
        <Row
          label={t("orderDetail.params.created")}
          value={new Date(order.created_at).toLocaleString()}
        />
        <Row
          label={t("orderDetail.params.expires")}
          value={new Date(order.expires_at).toLocaleString()}
        />
      </CardContent>
    </Card>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-0.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="slashed-zero">{value}</span>
    </div>
  );
}
