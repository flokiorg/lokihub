import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import {
  BotIcon,
  CirclePlusIcon,
  PauseIcon,
  ShieldAlertIcon,
} from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";
import { toast } from "sonner";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Switch } from "src/components/ui/switch";
import {
  AI_GROUPS,
  AIAssistant,
  useAIAssistants,
} from "src/hooks/useAIAssistants";

dayjs.extend(relativeTime);

const groupTitle = (id: string) =>
  AI_GROUPS.find((g) => g.id === id)?.title ?? id;

// The "AI assistants" tab of Connections: the owner's global pause switch, the
// approval queue for irreversible actions an assistant asked for, and one row
// per assistant connection (granted areas, last used, revoke).
export default function AIAssistants() {
  const { t } = useTranslation("apps");
  const {
    assistants,
    pending,
    controls,
    loading,
    revoke,
    decide,
    updateControls,
  } = useAIAssistants();

  async function run(fn: () => Promise<void>, failMsg: string) {
    try {
      await fn();
    } catch (err) {
      toast.error(failMsg, { description: (err as Error).message });
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <Card>
        <CardHeader className="flex flex-row items-center justify-between gap-4">
          <div className="flex flex-col gap-1.5">
            <CardTitle className="flex items-center gap-2">
              <PauseIcon className="w-4 h-4" />
              {t("assistants.pauseTitle", "Pause all AI assistants")}
            </CardTitle>
            <CardDescription>
              {t(
                "assistants.pauseDescription",
                "Stops every assistant immediately, including ones mid-conversation. Their connections stay until you turn this off."
              )}
            </CardDescription>
          </div>
          <Switch
            checked={!!controls.paused}
            aria-label={t("assistants.pauseTitle", "Pause all AI assistants")}
            onCheckedChange={(v) =>
              run(
                () => updateControls({ paused: v }),
                t("assistants.pauseFailed", "Could not change the pause switch")
              )
            }
          />
        </CardHeader>
      </Card>

      {pending.length > 0 && (
        <Card className="border-warning-foreground/40">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <ShieldAlertIcon className="w-4 h-4 text-warning-foreground" />
              {t("assistants.pendingTitle", "Waiting for your approval")}
            </CardTitle>
            <CardDescription>
              {t(
                "assistants.pendingDescription",
                "These actions can't be undone, so an assistant can only ask. Approve only what you asked it to do."
              )}
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col divide-y divide-border">
            {pending.map((p) => (
              <div
                key={p.id}
                className="flex flex-wrap items-center justify-between gap-3 py-3 first:pt-0"
              >
                <div className="flex flex-col">
                  <span className="font-medium">{p.summary}</span>
                  <span className="text-sm text-muted-foreground">
                    {assistants.find((a) => a.appId === p.appId)?.name ??
                      t("assistants.unknown", "An assistant")}
                  </span>
                </div>
                <div className="flex gap-2">
                  <Button
                    variant="outline"
                    onClick={() =>
                      run(
                        () => decide(p.id, "deny"),
                        t("assistants.denyFailed", "Could not deny")
                      )
                    }
                  >
                    {t("assistants.deny", "Deny")}
                  </Button>
                  <Button
                    onClick={() =>
                      run(
                        () => decide(p.id, "approve"),
                        t("assistants.approveFailed", "Could not approve")
                      )
                    }
                  >
                    {t("assistants.approve", "Approve")}
                  </Button>
                </div>
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      <div className="flex items-center justify-between gap-4">
        <h2 className="text-lg font-semibold">
          {t("assistants.listTitle", "Your AI assistants")}
        </h2>
        <Link to="/apps/new/assistant">
          <Button variant="outline">
            <CirclePlusIcon className="w-4 h-4" />
            {t("assistants.connect", "Connect an AI assistant")}
          </Button>
        </Link>
      </div>

      {!loading && assistants.length === 0 && (
        <Card>
          <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
            <BotIcon className="w-8 h-8 text-muted-foreground" />
            <p className="max-w-md text-sm text-muted-foreground">
              {t(
                "assistants.empty",
                "Connect an AI assistant to manage your hub by chatting with it. You choose exactly what it can see and do."
              )}
            </p>
          </CardContent>
        </Card>
      )}

      {assistants.map((a) => (
        <AssistantRow
          key={a.appId}
          assistant={a}
          onRevoke={() =>
            run(
              () => revoke(a.appPubkey),
              t("assistants.revokeFailed", "Could not revoke")
            )
          }
        />
      ))}
    </div>
  );
}

function AssistantRow({
  assistant,
  onRevoke,
}: {
  assistant: AIAssistant;
  onRevoke: () => void;
}) {
  const { t } = useTranslation("apps");
  const [confirming, setConfirming] = React.useState(false);
  const expired =
    assistant.expiresAt && dayjs(assistant.expiresAt).isBefore(dayjs());
  // Canonical group order (not map-key order) so every row reads the same way.
  const groups = AI_GROUPS.filter((g) => assistant.groups[g.id]).map(
    (g) => [g.id, assistant.groups[g.id]] as const
  );

  return (
    <Card>
      <CardContent className="flex flex-wrap items-center justify-between gap-4">
        <div className="flex items-center gap-3 min-w-0">
          <div className="rounded-full bg-muted p-2">
            <BotIcon className="w-5 h-5" />
          </div>
          <div className="flex flex-col min-w-0">
            <div className="flex items-center gap-2">
              <span className="font-medium truncate">{assistant.name}</span>
              <Badge variant={expired ? "warning" : "positive"}>
                {expired
                  ? t("assistants.expired", "Expired")
                  : t("assistants.aiAssistant", "AI assistant")}
              </Badge>
            </div>
            <span className="text-sm text-muted-foreground">
              {assistant.lastUsedAt
                ? t("assistants.used", "Used {{when}}", {
                    when: dayjs(assistant.lastUsedAt).fromNow(),
                  })
                : t("assistants.neverUsed", "Never used")}
            </span>
            <div className="flex flex-wrap gap-1 mt-1">
              {groups.map(([g, m]) => (
                <Badge key={g} variant="outline">
                  {groupTitle(g)} ·{" "}
                  {m === "write"
                    ? t("assistant.write", "Read & change")
                    : t("assistant.read", "Read")}
                </Badge>
              ))}
            </div>
          </div>
        </div>
        {confirming ? (
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => setConfirming(false)}>
              {t("assistants.keep", "Keep")}
            </Button>
            <Button variant="destructive" onClick={onRevoke}>
              {t("assistants.confirmRevoke", "Revoke now")}
            </Button>
          </div>
        ) : (
          <Button variant="outline" onClick={() => setConfirming(true)}>
            {t("assistants.revoke", "Revoke")}
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
