import { WebhookIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
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
  const { t } = useTranslation("lsp");
  return (
    <div className="space-y-4">
      <AppHeader
        title={t("webhooks.title")}
        description={t("webhooks.description")}
      />

      <Alert>
        <WebhookIcon className="size-4" />
        <AlertTitle>{t("webhooks.alert.title")}</AlertTitle>
        <AlertDescription>{t("webhooks.alert.description")}</AlertDescription>
      </Alert>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base">
            {t("webhooks.transports.title")}
          </CardTitle>
          <CardDescription>
            {t("webhooks.transports.description")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3 text-sm">
          <div className="grid gap-0.5">
            <span className="font-medium">
              {t("webhooks.transports.push.title")}
            </span>
            <span className="text-muted-foreground">
              {t("webhooks.transports.push.description")}
            </span>
          </div>
          <div className="grid gap-0.5">
            <span className="font-medium">
              {t("webhooks.transports.nostr.title")}
            </span>
            <span className="text-muted-foreground">
              {t("webhooks.transports.nostr.description")}
            </span>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="text-base">
            {t("webhooks.limits.title")}
          </CardTitle>
          <CardDescription>{t("webhooks.limits.description")}</CardDescription>
        </CardHeader>
      </Card>
    </div>
  );
}
