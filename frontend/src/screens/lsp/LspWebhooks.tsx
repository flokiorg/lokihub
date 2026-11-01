import { WebhookIcon } from "lucide-react";
import AppHeader from "src/components/AppHeader";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";

// LSPS5 webhooks (set_webhook / list_webhooks / remove_webhook) are served to
// clients over the LSPS protocol on the flnd custom-message transport, NOT over
// lspd's operator dashboard JSON-RPC. The dashboard exposes no per-webhook
// registry, and the webhook limits (LSPS5_MAX_WEBHOOKS, loopback opt-in) are
// daemon env, not node config — so this screen is informational rather than a
// live registry. Wiring a registry would require a new lspd dashboard method
// (flagged for a follow-up; see the migration parity inventory, Webhooks GAP).
export function LspWebhooks() {
  return (
    <div className="space-y-4">
      <AppHeader
        title="Webhooks"
        description="LSPS5 client notification webhooks"
      />

      <Alert>
        <WebhookIcon className="size-4" />
        <AlertTitle>Managed by clients over the LSPS protocol</AlertTitle>
        <AlertDescription>
          Clients register and remove their own LSPS5 webhooks over the LSPS
          transport (bLIP-55). The LSP operator dashboard API does not expose a
          per-webhook registry, so there is nothing to list or edit here yet.
        </AlertDescription>
      </Alert>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base">Transports</CardTitle>
          <CardDescription>
            How notifications reach a client once it has registered a webhook.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3 text-sm">
          <div className="grid gap-0.5">
            <span className="font-medium">push (HTTPS)</span>
            <span className="text-muted-foreground">
              A signed POST to the client&apos;s https:// URL. Dial-time SSRF
              guard rejects private addresses unless loopback webhooks are
              explicitly enabled on the daemon.
            </span>
          </div>
          <div className="grid gap-0.5">
            <span className="font-medium">nostr</span>
            <span className="text-muted-foreground">
              An encrypted event (kind 31055) to the client&apos;s
              nostr+lsps5:// relay. Delivered through the daemon&apos;s relay
              pool.
            </span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base">Limits</CardTitle>
          <CardDescription>
            Webhook limits are configured on the LSP daemon via environment
            (LSPD_LSPS5_MAX_WEBHOOKS, LSPD_LSPS5_ALLOW_LOOPBACK_WEBHOOKS) and
            are not editable from the Hub.
          </CardDescription>
        </CardHeader>
      </Card>
    </div>
  );
}
