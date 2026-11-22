import { BotIcon, ShieldCheckIcon, TriangleAlertIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import AssistantGroupPicker from "src/components/connections/AssistantGroupPicker";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { AssistantLevels, levelsToGrants } from "src/hooks/useAIAssistants";
import { request } from "src/utils/request";

type ConsentView = {
  clientName: string;
  redirectHost: string;
  redirectUri: string;
  domain: string;
  pathTenant: boolean;
};

// Read-only by default for a remote request: the owner opts in to more.
const CONSENT_DEFAULT: AssistantLevels = {
  wallet: "read",
  cash_circles: "off",
  channels: "off",
  listings: "off",
  node: "read",
  connections: "off",
};

// Owner consent for a remote AI assistant connecting by URL (OAuth). The page
// only knows an opaque consent id: the client, redirect and PKCE details were
// validated and parked by the hub at /oauth/authorize, so nothing here can be
// tampered with to send the result elsewhere.
export function ConsentAssistant() {
  const { t } = useTranslation("apps");
  const [params] = useSearchParams();
  const id = params.get("consent") ?? "";
  const [view, setView] = React.useState<ConsentView>();
  const [error, setError] = React.useState<string>();
  const [name, setName] = React.useState("");
  const [levels, setLevels] = React.useState<AssistantLevels>(CONSENT_DEFAULT);
  const [busy, setBusy] = React.useState(false);
  const [expiresInDays, setExpiresInDays] = React.useState(0);
  const grants = React.useMemo(() => levelsToGrants(levels), [levels]);

  React.useEffect(() => {
    if (!id) {
      setError(t("consent.missing", "This link is incomplete."));
      return;
    }
    request<ConsentView>(`/api/mcp/oauth/consent/${encodeURIComponent(id)}`)
      .then((v) => {
        setView(v);
        // Never pre-fill the attacker-choosable client name (pentest E MED-1/2):
        // default to where the access goes, which the hub verified.
        setName(v?.domain ? `Assistant via ${v.domain}` : "");
      })
      .catch((e: Error) => setError(e.message));
  }, [id, t]);

  async function decide(kind: "approve" | "deny") {
    setBusy(true);
    try {
      const res = await request<{ redirect: string }>(
        `/api/mcp/oauth/consent/${encodeURIComponent(id)}/${kind}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body:
            kind === "approve"
              ? JSON.stringify({
                  name: name.trim(),
                  groups: grants,
                  expiresInDays,
                })
              : undefined,
        }
      );
      if (res?.redirect) {
        window.location.assign(res.redirect);
      }
    } catch (e) {
      toast.error(t("consent.failed", "Could not complete the request"), {
        description: (e as Error).message,
      });
      setBusy(false);
    }
  }

  if (error) {
    return (
      <>
        <AppHeader title={t("consent.title", "Connect an AI assistant")} />
        <Card className="max-w-2xl">
          <CardContent className="py-8 text-sm text-muted-foreground">
            {error}
          </CardContent>
        </Card>
      </>
    );
  }
  if (!view) {
    return null;
  }

  const nothing = Object.keys(grants).length === 0;
  return (
    <>
      <AppHeader
        title={t("consent.title", "Connect an AI assistant")}
        description={t(
          "consent.description",
          "An AI assistant is asking to use your hub. Choose what it can do — you can change or revoke this at any time."
        )}
      />
      <div className="flex flex-col gap-6 max-w-2xl">
        <Card>
          <CardHeader>
            <CardDescription>
              {t("consent.accessGoesTo", "Access goes to")}
            </CardDescription>
            <CardTitle className="flex items-center gap-2 text-2xl break-all">
              <BotIcon className="w-5 h-5 shrink-0" />
              {view.domain || view.redirectHost}
            </CardTitle>
            <CardDescription className="break-all font-mono text-xs">
              {view.redirectUri}
            </CardDescription>
            {view.pathTenant && (
              <Alert>
                <TriangleAlertIcon className="w-4 h-4" />
                <AlertTitle>
                  {t(
                    "consent.sharedHostTitle",
                    "Shared address — check who it belongs to"
                  )}
                </AlertTitle>
                <AlertDescription>
                  {t(
                    "consent.sharedHostBody",
                    "This callback is a page on a site that hosts many people's pages, so the site name alone doesn't tell you who receives access. Only continue if you recognise this exact address."
                  )}
                </AlertDescription>
              </Alert>
            )}
            <CardDescription className="flex flex-col gap-1">
              <span>
                {t("consent.callsItself", "It calls itself")}{" "}
                <span className="font-medium text-foreground">
                  “{view.clientName}”
                </span>{" "}
                <Badge variant="warning">
                  {t("consent.unverified", "name not verified")}
                </Badge>
              </span>
              <span>
                {t(
                  "consent.redirectNote2",
                  "Only continue if you started this from your assistant just now and the address above is the one you expect."
                )}
              </span>
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-1.5">
            <Label htmlFor="consent-name">
              {t("consent.nameLabel", "Name in your connections")}
            </Label>
            <Input
              id="consent-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <Label className="mt-3">
              {t("consent.expiryLabel", "Access ends")}
            </Label>
            <div className="flex gap-2">
              {[0, 30, 90].map((d) => (
                <Button
                  key={d}
                  type="button"
                  size="sm"
                  variant={expiresInDays === d ? "default" : "outline"}
                  onClick={() => setExpiresInDays(d)}
                >
                  {d === 0
                    ? t("consent.expiryNever", "Never")
                    : t("consent.expiryDays", {
                        defaultValue: "In {{d}} days",
                        d,
                      })}
                </Button>
              ))}
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>
              {t("assistant.permissionsTitle", "What it can do")}
            </CardTitle>
            <CardDescription>
              {t(
                "assistant.permissionsDescription",
                "Read lets it look. Read & change lets it act for you in that area, like you would in the app. Your recovery phrase, backups and node commands are never available to it."
              )}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <AssistantGroupPicker levels={levels} onChange={setLevels} />
          </CardContent>
        </Card>
        <div className="flex gap-2">
          <Button
            disabled={busy || nothing || !name.trim()}
            onClick={() => decide("approve")}
          >
            <ShieldCheckIcon className="w-4 h-4" />
            {t("consent.approve", "Allow")}
          </Button>
          <Button
            variant="outline"
            disabled={busy}
            onClick={() => decide("deny")}
          >
            {t("consent.deny", "Deny")}
          </Button>
        </div>
      </div>
    </>
  );
}
