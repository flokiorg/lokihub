import dayjs from "dayjs";
import isBetween from "dayjs/plugin/isBetween";
import relativeTime from "dayjs/plugin/relativeTime";
import { CalendarClockIcon, ChevronRightIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import AppAvatar from "src/components/AppAvatar";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import { Badge } from "src/components/ui/badge";
import { App } from "src/types";

dayjs.extend(relativeTime);
dayjs.extend(isBetween);

// A connected (external NWC) app rendered as one row in the same visual
// language as the Cash Hubs / Circles lists: avatar + name + last-used
// subtitle, a status pill (Connected, or an Expired / Expires-soon warning
// carried over from AppCardNotice), a compact trailing value (budget left, or
// amount spent, or Read-only), and a chevron. The whole row is the link; the
// AppCard (used elsewhere for sub-wallets) is left unchanged.
export default function AppRow({ app }: { app: App }) {
  const { t } = useTranslation("apps");

  const now = dayjs();
  const expiresAt = app.expiresAt ? dayjs(app.expiresAt) : null;
  const isExpired = expiresAt ? expiresAt.isBefore(now) : false;
  const expiresSoon = expiresAt
    ? expiresAt.isBetween(now, now.add(7, "days"))
    : false;

  const hasBudget = app.maxAmount > 0;
  const canPay = app.scopes.indexOf("pay_invoice") > -1;

  const trailing = hasBudget ? (
    <div className="text-end">
      <p className="text-muted-foreground text-xs">{t("usage.leftInBudget")}</p>
      <p className="sensitive slashed-zero text-sm font-medium">
        <FormattedFlokicoinAmount
          amount={(app.maxAmount - app.budgetUsage) * 1000}
        />
      </p>
    </div>
  ) : canPay ? (
    <div className="text-end">
      <p className="text-muted-foreground text-xs">{t("connections.rowSpent")}</p>
      <p className="sensitive slashed-zero text-sm font-medium">
        <FormattedFlokicoinAmount amount={app.budgetUsage * 1000} />
      </p>
    </div>
  ) : (
    <div className="text-end">
      <p className="text-muted-foreground text-xs">
        {t("connections.rowReadOnly")}
      </p>
    </div>
  );

  return (
    <Link
      to={`/apps/${app.id}`}
      className="hover:border-primary/50 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 rounded-xl border bg-card p-3 transition-colors sm:flex-nowrap"
    >
      <AppAvatar app={app} className="size-8 shrink-0" />
      <div className="min-w-0 flex-1">
        <p className="truncate font-medium">{app.name}</p>
        <p className="text-muted-foreground text-xs">
          {app.lastUsedAt
            ? t("connections.rowLastUsed", { time: dayjs(app.lastUsedAt).fromNow() })
            : t("connections.rowNeverUsed")}
        </p>
      </div>
      <div className="flex w-full items-center justify-between gap-3 sm:w-auto sm:justify-end">
        {isExpired ? (
          <Badge variant="destructive">
            <CalendarClockIcon className="size-3" />
            {t("connections.rowExpired")}
          </Badge>
        ) : expiresSoon ? (
          <Badge variant="outline">
            <CalendarClockIcon className="size-3" />
            {t("connections.rowExpiresSoon")}
          </Badge>
        ) : (
          <Badge variant="positive">{t("connections.connected")}</Badge>
        )}
        {trailing}
        <ChevronRightIcon className="text-muted-foreground size-4 shrink-0" />
      </div>
    </Link>
  );
}
