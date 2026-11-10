import { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { Badge } from "src/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { LSPServiceInfo } from "src/types";

// LspServiceStatusCard mirrors PrivateTransportStatusCard: it shows whether the
// Hub is attached to its lspd daemon (plan §3.4). "Attached" means lspd's /info
// answered AND the operator's admin key (nsec/bunker) was accepted; a
// configured-but-unreachable lspd shows "Unreachable" with the error, and a
// key that is not lspd's DASHBOARD_ADMIN_PUBKEY shows "key mismatch (403)".
export function LspServiceStatusCard({
  status,
  className,
}: {
  status: LSPServiceInfo;
  className?: string;
}) {
  const { t } = useTranslation("lsp");
  const { label, variant } = badgeFor(status, t);
  return (
    <Card className={className}>
      <CardHeader className="pb-3">
        <CardTitle className="text-lg flex items-center gap-2">
          {t("serviceCard.title")}
          <Badge variant={variant}>{label}</Badge>
        </CardTitle>
        <CardDescription>{t("serviceCard.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        {status.endpoint && (
          <Row label={t("serviceCard.endpoint")} value={status.endpoint} mono />
        )}
        {status.alias && (
          <Row label={t("serviceCard.node")} value={status.alias} />
        )}
        {status.version && (
          <Row label={t("serviceCard.version")} value={status.version} />
        )}
        {status.pubkey && (
          <Row
            label={t("serviceCard.adminPubkey")}
            value={status.pubkey}
            mono
            truncate
          />
        )}
        {status.error && (
          <p className="text-destructive">{errorText(status.error, t)}</p>
        )}
      </CardContent>
    </Card>
  );
}

function Row({
  label,
  value,
  mono,
  truncate,
}: {
  label: string;
  value: string;
  mono?: boolean;
  truncate?: boolean;
}) {
  return (
    <div className="grid gap-1">
      <span className="text-muted-foreground">{label}</span>
      <span
        className={[
          mono ? "font-mono" : "",
          truncate ? "truncate block" : "break-all",
        ].join(" ")}
      >
        {value}
      </span>
    </div>
  );
}

function badgeFor(
  status: LSPServiceInfo,
  t: TFunction<"lsp">
): {
  label: string;
  variant: "positive" | "destructive" | "secondary" | "warning";
} {
  if (!status.configured) {
    return {
      label: t("serviceCard.badge.notConfigured"),
      variant: "secondary",
    };
  }
  if (status.attached) {
    return { label: t("serviceCard.badge.attached"), variant: "positive" };
  }
  if (status.reachable) {
    return { label: t("serviceCard.badge.reachable"), variant: "warning" };
  }
  return { label: t("serviceCard.badge.unreachable"), variant: "destructive" };
}

function errorText(error: string, t: TFunction<"lsp">): string {
  if (error === "unreachable") {
    return t("serviceCard.error.unreachable");
  }
  if (error === "key mismatch (403)") {
    return t("serviceCard.error.keyMismatch");
  }
  return error;
}
