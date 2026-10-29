import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import useSWR from "swr";
import { Badge } from "src/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "src/components/ui/dialog";
import {
  cancelNote,
  depthLabel,
  feeRate,
  loki,
  outpoint,
  overdue,
  shorten,
  stateVariant,
  type PayoutDetail,
  type PayoutRow,
} from "src/components/withdraw/payouts";
import { cn } from "src/lib/utils";
import { swrFetcher } from "src/utils/swr";

const when = (secs?: number) =>
  secs ? new Date(secs * 1000).toLocaleString() : "—";

// CashHubPayoutsCard lists a Cash Hub's on-chain payouts, newest first, and
// opens one with every version and archive fact. It offers no action: no
// operator action changes a payout (WP-19 item 10).
export function CashHubPayoutsCard({ hubId }: { hubId: number }) {
  const { t } = useTranslation("settings");
  const { data: rows, error } = useSWR<PayoutRow[]>(
    `/api/cash-hubs/${hubId}/payouts`,
    swrFetcher
  );
  const [open, setOpen] = useState<number | null>(null);
  // The clock the overdue mark is read against, taken once per mount.
  const [now] = useState(() => Math.floor(Date.now() / 1000));
  if (error || !rows || rows.length === 0) {
    // No withdrawal service, or nothing paid out yet.
    return null;
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("withdraw.payouts.title")}</CardTitle>
        <CardDescription>{t("withdraw.payouts.description")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-2">
        {rows.map((p) => {
          const cancel = cancelNote(p);
          const op = outpoint(p);
          return (
            <button
              key={p.id}
              type="button"
              className="grid gap-1 rounded-md border p-2 text-start text-sm hover:bg-accent/50"
              onClick={() => setOpen(p.id)}
            >
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant={stateVariant(p.state)}>
                  {t(`withdraw.payouts.state.${p.state}`)}
                </Badge>
                <span className="font-mono">{shorten(p.address)}</span>
                <span className="ms-auto tabular-nums">
                  {loki(p.payout_millis)}
                </span>
              </div>
              <div className="flex flex-wrap gap-x-4 gap-y-1 text-muted-foreground">
                <span>
                  {t("withdraw.payouts.amount")} {loki(p.amount_millis)}
                </span>
                <span>
                  {t("withdraw.payouts.fee")} {loki(p.fee_millis)}
                </span>
                <span className={cn(overdue(p, now) && "text-destructive")}>
                  {t("withdraw.payouts.sentBy")} {when(p.sent_by)}
                </span>
                <span>
                  {p.signed
                    ? t("withdraw.payouts.signed")
                    : t("withdraw.payouts.unsigned")}
                </span>
                {op && <span className="font-mono">{op}</span>}
                <span>
                  {t("withdraw.payouts.depth")} {depthLabel(p)}
                </span>
                {cancel && (
                  <span>
                    {t(`withdraw.payouts.cancel.${cancel.key}`, {
                      reason: cancel.reason ?? "",
                    })}
                  </span>
                )}
                {p.remint_app_id && (
                  <Link
                    to={`/apps/${p.remint_app_id}`}
                    className="underline"
                    onClick={(e) => e.stopPropagation()}
                  >
                    {t("withdraw.payouts.remintedBill")}
                  </Link>
                )}
              </div>
            </button>
          );
        })}
      </CardContent>
      <PayoutDetailDialog
        hubId={hubId}
        payoutId={open}
        onClose={() => setOpen(null)}
      />
    </Card>
  );
}

function PayoutDetailDialog({
  hubId,
  payoutId,
  onClose,
}: {
  hubId: number;
  payoutId: number | null;
  onClose: () => void;
}) {
  const { t } = useTranslation("settings");
  const { data: d } = useSWR<PayoutDetail>(
    payoutId ? `/api/cash-hubs/${hubId}/payouts/${payoutId}` : null,
    swrFetcher
  );
  return (
    <Dialog open={payoutId !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("withdraw.payouts.detailTitle")}</DialogTitle>
          <DialogDescription>
            {d && t(`withdraw.payouts.state.${d.state}`)}
          </DialogDescription>
        </DialogHeader>
        {d && (
          <div className="grid gap-4 text-sm">
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
              <dt className="text-muted-foreground">
                {t("withdraw.payouts.address")}
              </dt>
              <dd className="font-mono break-all">{d.address}</dd>
              <dt className="text-muted-foreground">
                {t("withdraw.payouts.amount")}
              </dt>
              <dd>{loki(d.amount_millis)}</dd>
              <dt className="text-muted-foreground">
                {t("withdraw.payouts.fee")}
              </dt>
              <dd>
                {loki(d.fee_millis)} ({t("withdraw.payouts.hubFee")}{" "}
                {loki(d.hub_fee_millis)}, {t("withdraw.payouts.networkFee")}{" "}
                {loki(d.network_fee_millis)})
              </dd>
              <dt className="text-muted-foreground">
                {t("withdraw.payouts.payout")}
              </dt>
              <dd>{loki(d.payout_millis)}</dd>
              <dt className="text-muted-foreground">
                {t("withdraw.payouts.requestId")}
              </dt>
              <dd className="font-mono break-all">{d.request_id}</dd>
              <dt className="text-muted-foreground">
                {t("withdraw.payouts.committed")}
              </dt>
              <dd>{when(d.committed_at)}</dd>
              <dt className="text-muted-foreground">
                {t("withdraw.payouts.sentBy")}
              </dt>
              <dd>{when(d.sent_by)}</dd>
              <dt className="text-muted-foreground">
                {t("withdraw.payouts.depth")}
              </dt>
              <dd>{depthLabel(d)}</dd>
            </dl>
            <div className="grid gap-2">
              <h4 className="font-medium">{t("withdraw.payouts.versions")}</h4>
              {d.versions.length === 0 && (
                <p className="text-muted-foreground">
                  {t("withdraw.payouts.noVersions")}
                </p>
              )}
              {d.versions.map((v) => (
                <div key={v.seq} className="rounded-md border p-2 grid gap-1">
                  <div className="flex flex-wrap gap-2">
                    <span>#{v.seq}</span>
                    <span>{v.kind}</span>
                    <Badge variant="outline">{v.state}</Badge>
                    {v.pays && v.vout != null && v.txid && (
                      <span className="font-mono">
                        {shorten(v.txid)}:{v.vout}
                      </span>
                    )}
                  </div>
                  <div className="text-muted-foreground flex flex-wrap gap-x-4">
                    {feeRate(v) && <span>{feeRate(v)}</span>}
                    {v.fee_sat != null && <span>{v.fee_sat} loki</span>}
                    <span>
                      {t("withdraw.payouts.inputs", { count: v.inputs.length })}
                    </span>
                    <span>
                      {t("withdraw.payouts.broadcast")}{" "}
                      {when(v.first_broadcast_at)} / {when(v.last_broadcast_at)}
                    </span>
                  </div>
                </div>
              ))}
            </div>
            <div className="grid gap-1">
              <h4 className="font-medium">{t("withdraw.payouts.facts")}</h4>
              {d.facts.map((f, i) => (
                <div key={i} className="flex flex-wrap gap-x-4">
                  <span>{when(f.at)}</span>
                  <span>{f.state}</span>
                  {f.txid && (
                    <span className="font-mono">
                      {shorten(f.txid)}
                      {f.vout != null && `:${f.vout}`}
                    </span>
                  )}
                  {f.miner_fee_millis != null && (
                    <span>{loki(f.miner_fee_millis)}</span>
                  )}
                </div>
              ))}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
