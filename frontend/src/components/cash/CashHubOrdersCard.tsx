import { useTranslation } from "react-i18next";
import {
  type OrderPaymentRow,
  type OrderRow,
  orderStateVariant,
  paymentStateVariant,
} from "src/components/cash/orders";
import { useNativeAmountFormat } from "src/hooks/useNativeAmountFormat";
import { Badge } from "src/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { useOrders } from "src/hooks/useCashOrders";

const when = (secs: number) =>
  secs ? new Date(secs * 1000).toLocaleString() : "—";
const short = (s: string) =>
  s.length <= 15 ? s : `${s.slice(0, 8)}…${s.slice(-6)}`;

// CashHubOrdersCard lists a Cash Hub's placed orders newest first and the
// payments it kept with no order (NIP-CO / WP-19 item 8). It shows operator
// facts only — no cash token and no buyer key.
export function CashHubOrdersCard({ hubId }: { hubId: number }) {
  const { t } = useTranslation("settings");
  const { loki } = useNativeAmountFormat();
  const { data: view, error } = useOrders(hubId);
  if (error || !view || (view.orders.length === 0 && view.kept.length === 0)) {
    return null;
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("sellCash.orders.title")}</CardTitle>
        <CardDescription>{t("sellCash.orders.description")}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-2">
        {view.orders.map((o) => (
          <OrderRowView key={o.id} order={o} />
        ))}
        {view.kept.length > 0 && (
          <div className="grid gap-2 mt-2">
            <h4 className="font-medium text-sm">{t("sellCash.orders.keptTitle")}</h4>
            <p className="text-sm text-muted-foreground">
              {t("sellCash.orders.keptDescription")}
            </p>
            {view.kept.map((k, i) => (
              <div
                key={i}
                className="flex flex-wrap items-center gap-2 rounded-md border p-2 text-sm"
              >
                <Badge variant={paymentStateVariant(k.state)}>
                  {t(`sellCash.orders.paymentState.${k.state}`, k.state)}
                </Badge>
                <span className="tabular-nums">{loki(k.value_millis)}</span>
                <span className="ms-auto text-muted-foreground">
                  {when(k.seen_at)}
                </span>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function OrderRowView({ order: o }: { order: OrderRow }) {
  const { t } = useTranslation("settings");
  const { loki } = useNativeAmountFormat();
  return (
    <div className="grid gap-1.5 rounded-md border p-2 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={orderStateVariant(o.state)}>
          {t(`sellCash.orders.state.${o.state}`, o.state)}
        </Badge>
        <Badge variant="outline">
          {t(`sellCash.orders.kind.${o.payment}`, o.payment)}
        </Badge>
        <span className="text-muted-foreground">#{o.id}</span>
        <span className="ms-auto tabular-nums">{loki(o.pay_millis)}</span>
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-muted-foreground">
        <span>
          {t("sellCash.orders.amount")} {loki(o.amount_millis)}
        </span>
        <span>
          {t("sellCash.orders.fee")} {loki(o.fee_millis)}
        </span>
        <span>
          {t("sellCash.orders.payBy")} {when(o.pay_by)}
        </span>
        {o.confirmations != null && (
          <span>
            {t("sellCash.orders.requiredConfirmations")} {o.confirmations}
          </span>
        )}
        <span>
          {t("sellCash.orders.recipient")}{" "}
          {t(`sellCash.orders.recipientType.${o.recipient_type}`, o.recipient_type)}
        </span>
      </div>
      {o.payments.length > 0 && (
        <div className="grid gap-1 ps-2 border-s">
          {o.payments.map((p, i) => (
            <PaymentRowView key={i} payment={p} />
          ))}
        </div>
      )}
    </div>
  );
}

function PaymentRowView({ payment: p }: { payment: OrderPaymentRow }) {
  const { t } = useTranslation("settings");
  const { loki } = useNativeAmountFormat();
  const minted = p.minted_at != null;
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <Badge variant={paymentStateVariant(p.state)}>
        {t(`sellCash.orders.paymentState.${p.state}`, p.state)}
      </Badge>
      <span className="tabular-nums">{loki(p.value_millis)}</span>
      <span className="text-muted-foreground">
        {t("sellCash.orders.confirmations")} {p.confirmations}
      </span>
      {p.address && <span className="font-mono text-xs">{short(p.address)}</span>}
      {minted && (
        <span className="text-muted-foreground">
          {t("sellCash.orders.mintedBill")} {when(p.minted_at!)}
        </span>
      )}
    </div>
  );
}
