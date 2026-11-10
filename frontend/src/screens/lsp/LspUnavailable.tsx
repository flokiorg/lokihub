import { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { AppError } from "src/utils/request";

// LspUnavailable renders the graceful-degradation state (plan §3.4): when lspd
// is configured but unreachable or the admin key is rejected, the Hub proxy
// returns 503 (not configured) / 502 (unreachable or key mismatch). Screens
// render this instead of crashing, so the nav item can stay visible while lspd
// is down.
export function LspUnavailable({ error }: { error: unknown }) {
  const { t } = useTranslation("lsp");
  const { title, body } = describe(error, t);
  return (
    <Alert variant="warning">
      <AlertTitle>{title}</AlertTitle>
      <AlertDescription>{body}</AlertDescription>
    </Alert>
  );
}

function describe(
  error: unknown,
  t: TFunction<"lsp">
): { title: string; body: string } {
  if (error instanceof AppError) {
    if (error.status === 503) {
      return {
        title: t("unavailable.notConfigured.title"),
        body: t("unavailable.notConfigured.body"),
      };
    }
    if (error.status === 502) {
      return {
        title: t("unavailable.unreachable.title"),
        body: error.message || t("unavailable.unreachable.body"),
      };
    }
    return {
      title: t("unavailable.generic.title"),
      body: error.message || `HTTP ${error.status ?? ""}`.trim(),
    };
  }
  return {
    title: t("unavailable.generic.title"),
    body: t("unavailable.generic.body"),
  };
}
