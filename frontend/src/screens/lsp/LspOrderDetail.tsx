import { ArrowLeftIcon, CheckCircle2Icon } from "lucide-react";
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
  formatLoki,
  LspOrderTimelineEvent,
  orderStateVariant,
} from "src/screens/lsp/types";

export function LspOrderDetail() {
  const { orderId } = useParams<{ orderId: string }>();
  const { data: detail, error } = useLspOrderDetail(orderId);
  const { data: info } = useInfo();

  return (
    <div className="space-y-4">
      <AppHeader
        title="Order detail"
        description={orderId}
        contentRight={
          <Button variant="outline" size="sm" asChild>
            <Link to="/lsp/orders">
              <ArrowLeftIcon className="size-4" /> Back to orders
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
              payment: {detail.order.payment_state}
            </Badge>
            {detail.order.announce_channel && (
              <Badge variant="secondary">announced</Badge>
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
            The LSP dashboard API exposes order state read-only. Settlement and
            stuck-order recovery run automatically inside the LSP daemon, so
            there are no manual refund/reopen/recovery actions here.
          </p>
        </>
      )}
    </div>
  );
}

function TimelineCard({ events }: { events: LspOrderTimelineEvent[] }) {
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">Timeline</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        {events.map((e, i) => (
          <div key={i} className="flex items-start gap-3">
            <CheckCircle2Icon className="mt-0.5 size-4 text-primary" />
            <div className="grid gap-0.5">
              <span className="text-sm font-medium">{e.event}</span>
              <span className="text-xs text-muted-foreground">{e.detail}</span>
              <span className="text-xs text-muted-foreground slashed-zero">
                {new Date(e.timestamp).toLocaleString()}
              </span>
            </div>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

function PaymentCard({ invoice }: { invoice: string }) {
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">Payment</CardTitle>
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
            No invoice recorded for this order.
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
  if (!fundingTxid && !refundAddress) {
    return null;
  }
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">Channel</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        {fundingTxid && (
          <div className="grid gap-1">
            <span className="text-muted-foreground">
              Funding outpoint{" "}
              {fundingOutpoint !== null ? `(index ${fundingOutpoint})` : ""}
            </span>
            <div className="flex items-center gap-2">
              <span className="break-all font-mono text-xs">{fundingTxid}</span>
              {mempoolUrl && (
                <ExternalLinkButton
                  to={`${mempoolUrl}/tx/${fundingTxid}`}
                  variant="link"
                  className="h-auto p-0 text-xs"
                >
                  explorer
                </ExternalLinkButton>
              )}
            </div>
          </div>
        )}
        {refundAddress && (
          <div className="grid gap-1">
            <span className="text-muted-foreground">Refund address</span>
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
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">Parameters</CardTitle>
      </CardHeader>
      <CardContent className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-3">
        <Row label="LSP balance" value={formatLoki(order.lsp_balance_loki)} />
        <Row
          label="Client balance"
          value={formatLoki(order.client_balance_loki)}
        />
        <Row label="Order total" value={formatLoki(order.amount)} />
        <Row label="Fee" value={formatLoki(order.fee)} />
        <Row label="Reserved" value={formatLoki(order.reserved_amount_loki)} />
        <Row
          label="Channel expiry"
          value={`${order.channel_expiry_blocks} blocks`}
        />
        <Row
          label="Confirmations"
          value={String(order.required_channel_confirmations)}
        />
        <Row label="Peer" value={order.peer_id} mono />
        <Row
          label="Created"
          value={new Date(order.created_at).toLocaleString()}
        />
        <Row
          label="Expires"
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
