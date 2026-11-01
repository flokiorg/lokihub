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
  const { label, variant } = badgeFor(status);
  return (
    <Card className={className}>
      <CardHeader className="pb-3">
        <CardTitle className="text-lg flex items-center gap-2">
          LSP Service
          <Badge variant={variant}>{label}</Badge>
        </CardTitle>
        <CardDescription>
          The Hub reaches this LSP daemon over its NIP-98 dashboard. The
          backoffice below is served through the Hub; your browser never holds
          the admin key.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        {status.endpoint && (
          <Row label="Endpoint" value={status.endpoint} mono />
        )}
        {status.alias && <Row label="Node" value={status.alias} />}
        {status.version && <Row label="lspd version" value={status.version} />}
        {status.pubkey && (
          <Row label="Admin pubkey" value={status.pubkey} mono truncate />
        )}
        {status.error && (
          <p className="text-destructive">{errorText(status.error)}</p>
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

function badgeFor(status: LSPServiceInfo): {
  label: string;
  variant: "positive" | "destructive" | "secondary" | "warning";
} {
  if (!status.configured) {
    return { label: "Not configured", variant: "secondary" };
  }
  if (status.attached) {
    return { label: "Attached", variant: "positive" };
  }
  if (status.reachable) {
    return { label: "Reachable", variant: "warning" };
  }
  return { label: "Unreachable", variant: "destructive" };
}

function errorText(error: string): string {
  if (error === "unreachable") {
    return "The Hub could not reach the LSP daemon's dashboard.";
  }
  if (error === "key mismatch (403)") {
    return "The LSP daemon rejected the admin key: the Hub's nsec/bunker pubkey is not this node's DASHBOARD_ADMIN_PUBKEY.";
  }
  return error;
}
