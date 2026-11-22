import { BotIcon, CopyIcon, TriangleAlertIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import AssistantGroupPicker from "src/components/connections/AssistantGroupPicker";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
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
import { Tabs, TabsList, TabsTrigger } from "src/components/ui/tabs";
import {
  AIControls,
  AIGrants,
  CreatedAssistant,
  createAssistant,
  AssistantLevels,
  DEFAULT_ASSISTANT_LEVELS,
  levelsToGrants,
} from "src/hooks/useAIAssistants";
import { copyToClipboard } from "src/lib/clipboard";
import { request } from "src/utils/request";

// useEndpoint resolves where an assistant connects. In the desktop app the page
// origin (wails://…) is not reachable by an assistant: it connects to the
// local listener on 127.0.0.1, which must be switched on first (critic G1).
function useEndpoint(): { url?: string; desktop: boolean; loading: boolean } {
  const [c, setC] = React.useState<AIControls>();
  React.useEffect(() => {
    request<AIControls>("/api/mcp/controls")
      .then((v) => setC(v ?? {}))
      .catch(() => setC({}));
  }, []);
  if (!c) {
    return { desktop: false, loading: true };
  }
  if (c.loopbackSupported) {
    return {
      desktop: true,
      loading: false,
      url: c.loopbackPort
        ? `http://127.0.0.1:${c.loopbackPort}/mcp`
        : undefined,
    };
  }
  return {
    desktop: false,
    loading: false,
    url: `${window.location.origin}/mcp`,
  };
}

const EXPIRY_CHOICES = [
  { days: 0, label: "Never" },
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
];

const STARTER_PROMPT =
  "You're connected to my Lokihub wallet. Show me what you can do and my balances.";

export function NewAssistant() {
  const { t } = useTranslation("apps");
  const [name, setName] = React.useState("");
  const [levels, setLevels] = React.useState<AssistantLevels>(
    DEFAULT_ASSISTANT_LEVELS
  );
  const [expiryDays, setExpiryDays] = React.useState(0);
  const [submitting, setSubmitting] = React.useState(false);
  const [created, setCreated] = React.useState<CreatedAssistant>();

  const grants: AIGrants = React.useMemo(
    () => levelsToGrants(levels),
    [levels]
  );
  const nothingGranted = Object.keys(grants).length === 0;

  async function onCreate(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim() || nothingGranted) {
      return;
    }
    setSubmitting(true);
    try {
      const res = await createAssistant(name.trim(), grants, expiryDays);
      setCreated(res);
    } catch (err) {
      toast.error(
        t("assistant.createFailed", "Could not create the connection"),
        { description: (err as Error).message }
      );
    } finally {
      setSubmitting(false);
    }
  }

  if (created) {
    return <AssistantCreated created={created} name={name.trim()} />;
  }

  return (
    <>
      <AppHeader
        title={t("assistant.newTitle", "Connect an AI assistant")}
        description={t(
          "assistant.newDescription",
          "Let an AI assistant use your hub for you. Choose exactly what it can see and do — you can change or revoke this at any time."
        )}
      />
      <form onSubmit={onCreate} className="flex flex-col gap-6 max-w-2xl">
        <SignInByLink />
        <Card>
          <CardHeader>
            <CardTitle>{t("assistant.nameTitle", "Name")}</CardTitle>
            <CardDescription>
              {t(
                "assistant.nameDescription",
                "So you can recognise this assistant in your connections."
              )}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Label htmlFor="assistant-name" className="sr-only">
              {t("assistant.nameTitle", "Name")}
            </Label>
            <Input
              id="assistant-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("assistant.namePlaceholder", "My assistant")}
              autoFocus
              required
            />
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

        <Card>
          <CardHeader>
            <CardTitle>{t("assistant.expiryTitle", "Expires")}</CardTitle>
          </CardHeader>
          <CardContent>
            <Tabs
              value={String(expiryDays)}
              onValueChange={(v) => setExpiryDays(Number(v))}
            >
              <TabsList>
                {EXPIRY_CHOICES.map((c) => (
                  <TabsTrigger key={c.days} value={String(c.days)}>
                    {c.label}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          </CardContent>
        </Card>

        <div className="flex gap-2">
          <Button
            type="submit"
            disabled={submitting || !name.trim() || nothingGranted}
          >
            {t("assistant.create", "Create connection")}
          </Button>
          <Link to="/apps?tab=assistants">
            <Button type="button" variant="outline">
              {t("assistant.cancel", "Cancel")}
            </Button>
          </Link>
        </div>
        {nothingGranted && (
          <p className="text-sm text-muted-foreground">
            {t(
              "assistant.pickOne",
              "Turn on at least one area so the assistant can do something."
            )}
          </p>
        )}
      </form>
    </>
  );
}

function CopyRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label>{label}</Label>
      <div className="flex gap-2">
        <Input readOnly value={value} className="font-mono text-xs" />
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label={`Copy ${label}`}
          onClick={() => copyToClipboard(value)}
        >
          <CopyIcon className="w-4 h-4" />
        </Button>
      </div>
    </div>
  );
}

function AssistantCreated({
  created,
  name,
}: {
  created: CreatedAssistant;
  name: string;
}) {
  const { t } = useTranslation("apps");
  const endpoint = useEndpoint();
  const url = endpoint.url ?? "";
  const config = JSON.stringify(
    {
      mcpServers: {
        lokihub: {
          type: "http",
          url,
          headers: { Authorization: `Bearer ${created.token}` },
        },
      },
    },
    null,
    2
  );

  return (
    <>
      <AppHeader
        title={t("assistant.createdTitle", "Connect your assistant")}
        description={name}
      />
      <div className="flex flex-col gap-6 max-w-2xl">
        <Alert>
          <TriangleAlertIcon className="w-4 h-4" />
          <AlertTitle>{t("assistant.shownOnce", "Shown only once")}</AlertTitle>
          <AlertDescription>
            {t(
              "assistant.shownOnceBody",
              "Copy these details now. Anyone with them can use your hub within the permissions you chose — revoke the connection if they leak."
            )}
          </AlertDescription>
        </Alert>

        {endpoint.desktop && !endpoint.url && (
          <Alert>
            <TriangleAlertIcon className="w-4 h-4" />
            <AlertTitle>
              {t("assistant.desktopOffTitle", "Turn on local access first")}
            </AlertTitle>
            <AlertDescription>
              {t(
                "assistant.desktopOffBody",
                "Assistants on this computer connect to the Lokihub app directly. Switch on “Assistants on this computer” on the AI assistants page, then come back here — this key stays valid."
              )}{" "}
              <Link className="underline" to="/apps?tab=assistants">
                {t("assistant.openAssistants", "Open AI assistants")}
              </Link>
            </AlertDescription>
          </Alert>
        )}
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <BotIcon className="w-5 h-5" />
              {t("assistant.stepsTitle", "Add it to your AI assistant")}
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <ol className="list-decimal pl-5 text-sm flex flex-col gap-1">
              <li>
                {t("assistant.step1", "Open your AI assistant's settings.")}
              </li>
              <li>
                {t(
                  "assistant.step2",
                  "Find where it adds tools or connectors, and add one by URL."
                )}
              </li>
              <li>
                {t(
                  "assistant.step3",
                  "Paste the connector link and the access token below."
                )}
              </li>
            </ol>
            <CopyRow
              label={t("assistant.connectorLink", "Connector link")}
              value={url}
            />
            <CopyRow
              label={t("assistant.accessToken", "Access token")}
              value={created.token}
            />
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>
              {t("assistant.starterTitle", "Then say hello")}
            </CardTitle>
            <CardDescription>
              {t(
                "assistant.starterDescription",
                "Send this to your assistant to get started."
              )}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <CopyRow
              label={t("assistant.starterLabel", "Starter message")}
              value={STARTER_PROMPT}
            />
          </CardContent>
        </Card>

        <details className="rounded-lg border p-4">
          <summary className="cursor-pointer text-sm font-medium">
            {t("assistant.advancedSetup", "Advanced — manual config")}
          </summary>
          {endpoint.desktop && endpoint.url && (
            <div className="mt-3">
              <CopyRow
                label={t(
                  "assistant.stdioConfig",
                  "For assistants that only run local programs (stdio)"
                )}
                value={JSON.stringify(
                  {
                    mcpServers: {
                      lokihub: {
                        command: "lokihub-mcp",
                        args: ["--stdio", "--url", url],
                        env: { LOKIHUB_MCP_TOKEN: created.token },
                      },
                    },
                  },
                  null,
                  2
                )}
              />
            </div>
          )}
          <div className="mt-3 flex flex-col gap-2">
            <p className="text-sm text-muted-foreground">
              {t(
                "assistant.advancedBody",
                "For apps configured with a JSON file. Some apps name the top-level key differently (for example servers instead of mcpServers)."
              )}
            </p>
            <pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs">
              {config}
            </pre>
            <Button
              type="button"
              variant="outline"
              className="self-start"
              onClick={() => copyToClipboard(config)}
            >
              <CopyIcon className="w-4 h-4" />
              {t("assistant.copyConfig", "Copy config")}
            </Button>
          </div>
        </details>

        <p className="text-sm text-muted-foreground">
          {t(
            "assistant.onlineNote",
            "Your assistant can only act while your hub is running and unlocked."
          )}
        </p>
        <Link to="/apps?tab=assistants" className="self-start">
          <Button>{t("assistant.done", "Done")}</Button>
        </Link>
      </div>
    </>
  );
}

// SignInByLink: assistants that connect by link and ask you to approve (OAuth)
// need nothing created here — creating a key for them would leave an unused
// connection behind (critic E2).
function SignInByLink() {
  const { t } = useTranslation("apps");
  const endpoint = useEndpoint();
  if (endpoint.loading || endpoint.desktop || !endpoint.url) {
    return null;
  }
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {t("assistant.byLinkTitle", "Does your assistant connect by link?")}
        </CardTitle>
        <CardDescription>
          {t(
            "assistant.byLinkBody",
            "Many assistants only need this link: they then open a Lokihub page where you choose what they may do. In that case you don't need to create anything here."
          )}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <CopyRow
          label={t("assistant.connectorLink", "Connector link")}
          value={endpoint.url}
        />
      </CardContent>
    </Card>
  );
}
