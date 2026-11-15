import { ArrowLeftIcon, CheckCircle2Icon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link, useParams } from "react-router";
import AppHeader from "src/components/AppHeader";
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
import { LspUnavailable } from "src/screens/lsp/LspUnavailable";
import {
  LspOrderTimelineEvent,
  orderStateVariant,
} from "src/screens/lsp/types";
import { useFormatLoki } from "src/screens/lsp/useFormatLoki";

export function LspOrderDetail() {
  const { t } = useTranslation("lsp");
  const { orderId } = useParams<{ orderId: string }>();
  const { data: detail, error } = useLspOrderDetail(orderId);
  const { data: info } = useInfo();

  return (
    <div className="space-y-4">
      <AppHeader
        title={t("orderDetail.title")}
        description={orderId}
        contentRight={
          <Button variant="outline" size="sm" asChild>
            <Link to="/lsp/orders">
              <ArrowLeftIcon className="size-4" /> {t("orderDetail.back")}
            </Link>
          </Button>
        }
      />

      {error ? (
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
            <TimelineCard events={detail.timeline} />
            <PaymentCard invoice={detail.order.payment_invoice} />
          </div>

          <ChannelCard
            fundingTxid={detail.order.funding_txid}
            fundingOutpoint={detail.order.funding_outpoint}
            refundAddress={detail.order.refund_onchain_address}
            mempoolUrl={info?.mempoolUrl}
          />

          <ParamsCard order={detail.order} />

          <p className="text-xs text-muted-foreground">
            {t("orderDetail.readOnlyNote")}
          </p>
        </>
      )}
    </div>
  );
}

function TimelineCard({ events }: { events: LspOrderTimelineEvent[] }) {
  const { t } = useTranslation("lsp");
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
            <p className="break-all font-mono text-xs text-muted-foreground">
              {invoice}
            </p>
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
            <div className="flex items-center gap-2">
              <span className="break-all font-mono text-xs">{fundingTxid}</span>
              {mempoolUrl && (
                <ExternalLinkButton
                  to={`${mempoolUrl}/tx/${fundingTxid}`}
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
            <span className="break-all font-mono text-xs">{refundAddress}</span>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function ParamsCard({
  order,
}: {
  order: NonNullable<ReturnType<typeof useLspOrderDetail>["data"]>["order"];
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
      <CardContent className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-3">
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
        <Row label={t("orderDetail.params.peer")} value={order.peer_id} mono />
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

function Row({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="grid gap-0.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className={mono ? "break-all font-mono text-xs" : "slashed-zero"}>
        {value}
      </span>
    </div>
  );
}
