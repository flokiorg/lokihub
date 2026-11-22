import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";
import {
  ActivityIcon,
  BotIcon,
  CirclePlusIcon,
  LaptopIcon,
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
import { Input } from "src/components/ui/input";
import { Switch } from "src/components/ui/switch";
import AssistantGroupPicker from "src/components/connections/AssistantGroupPicker";
import {
  AI_GROUPS,
  AIActivity,
  AIAssistant,
  editAssistant,
  grantsToLevels,
  levelsToGrants,
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
    activity,
    loading,
    reload,
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

      <ApprovalLimitCard
        limit={controls.approvalLimitLoki ?? 10000}
        spent={controls.spentLoki24h ?? 0}
        onSave={(v) =>
          run(
            () => updateControls({ approvalLimitLoki: v }),
            t("assistants.limitFailed", "Could not save the approval limit")
          )
        }
      />

      {controls.loopbackSupported && (
        <LocalAccessCard
          port={controls.loopbackPort ?? 0}
          onApply={(port) =>
            run(
              () => updateControls({ loopbackPort: port }),
              t(
                "assistants.localFailed",
                "Could not change local access (is the port already in use?)"
              )
            )
          }
        />
      )}

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
                <PendingFrom
                  tool={p.tool}
                  summary={p.summary}
                  createdAt={p.createdAt}
                  appId={p.appId}
                  assistant={assistants.find((a) => a.appId === p.appId)}
                />
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
          onChanged={reload}
          onRevoke={() =>
            run(
              () => revoke(a.appPubkey),
              t("assistants.revokeFailed", "Could not revoke")
            )
          }
        />
      ))}

      {assistants.length > 0 && (
        <p className="text-sm text-muted-foreground">
          {t(
            "assistants.passwordNote",
            "Changing your hub password disconnects every assistant. To reconnect one, open Edit and rotate its key, then give the new key to the assistant."
          )}
        </p>
      )}

      {assistants.length > 0 && (
        <ActivityCard
          activity={activity}
          nameOf={(id) =>
            assistants.find((a) => a.appId === id)?.name ??
            t("assistants.unknown", "An assistant")
          }
        />
      )}
    </div>
  );
}

const OUTCOME_VARIANT: Record<
  AIActivity["outcome"],
  "positive" | "destructive" | "warning" | "secondary"
> = {
  done: "positive",
  failed: "destructive",
  needs_confirmation: "secondary",
  pending_approval: "warning",
  executing: "secondary",
  denied: "destructive",
  expired: "secondary",
};

// ActivityCard is the owner's record of what assistants did, newest first.
function ActivityCard({
  activity,
  nameOf,
}: {
  activity: AIActivity[];
  nameOf: (appId: number) => string;
}) {
  const { t } = useTranslation("apps");
  const outcomeLabel: Record<AIActivity["outcome"], string> = {
    done: t("assistants.outcome.done", "Done"),
    failed: t("assistants.outcome.failed", "Refused or failed"),
    needs_confirmation: t(
      "assistants.outcome.needsConfirmation",
      "Previewed, not confirmed"
    ),
    pending_approval: t(
      "assistants.outcome.pendingApproval",
      "Waiting for you"
    ),
    executing: t("assistants.outcome.executing", "Running"),
    denied: t("assistants.outcome.denied", "Denied"),
    expired: t("assistants.outcome.expired", "Expired"),
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <ActivityIcon className="w-4 h-4" />
          {t("assistants.activityTitle", "Recent activity")}
        </CardTitle>
        <CardDescription>
          {t(
            "assistants.activityDescription",
            "Everything your assistants did or tried, newest first. Each entry keeps a short description (such as an amount and recipient), not the full request. Older entries are in the hub log."
          )}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {activity.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t("assistants.activityEmpty", "Nothing yet.")}
          </p>
        ) : (
          <ul className="flex flex-col divide-y divide-border">
            {activity.map((e, i) => (
              <li
                key={`${e.time}-${i}`}
                className="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0"
              >
                <div className="flex min-w-0 flex-col">
                  <span className="font-medium">
                    {nameOf(e.appId)} · #{e.appId} · <code>{e.tool}</code>
                  </span>
                  {e.detail && (
                    <span className="truncate text-sm text-muted-foreground">
                      {e.detail}
                    </span>
                  )}
                </div>
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Badge variant={OUTCOME_VARIANT[e.outcome] ?? "secondary"}>
                    {outcomeLabel[e.outcome] ?? e.outcome}
                  </Badge>
                  {dayjs(e.time).fromNow()}
                </div>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

function AssistantRow({
  assistant,
  onRevoke,
  onChanged,
}: {
  assistant: AIAssistant;
  onRevoke: () => void;
  onChanged: () => void;
}) {
  const { t } = useTranslation("apps");
  const [confirming, setConfirming] = React.useState(false);
  const [editing, setEditing] = React.useState(false);
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
              <span className="text-sm text-muted-foreground">
                #{assistant.appId}
              </span>
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
          <div className="flex gap-2">
            <Button variant="outline" onClick={() => setEditing((v) => !v)}>
              {editing
                ? t("assistants.closeEdit", "Close")
                : t("assistants.edit", "Edit")}
            </Button>
            <Button variant="outline" onClick={() => setConfirming(true)}>
              {t("assistants.revoke", "Revoke")}
            </Button>
          </div>
        )}
      </CardContent>
      {editing && (
        <AssistantEditor
          assistant={assistant}
          onSaved={() => {
            setEditing(false);
            onChanged();
          }}
        />
      )}
    </Card>
  );
}

// AssistantEditor changes an assistant's name and what it may do (effective on
// its next call) and can rotate its key (the old key stops working at once).
function AssistantEditor({
  assistant,
  onSaved,
}: {
  assistant: AIAssistant;
  onSaved: () => void;
}) {
  const { t } = useTranslation("apps");
  const [name, setName] = React.useState(assistant.name);
  const [levels, setLevels] = React.useState(() =>
    grantsToLevels(assistant.groups)
  );
  const [busy, setBusy] = React.useState(false);
  const [newKey, setNewKey] = React.useState<string>();
  const grants = levelsToGrants(levels);
  const nothing = Object.keys(grants).length === 0;

  async function save() {
    setBusy(true);
    try {
      await editAssistant(assistant.appId, {
        name: name.trim(),
        groups: grants,
      });
      toast.success(
        t("assistants.saved", "Saved — applies from its next request")
      );
      onSaved();
    } catch (e) {
      toast.error(t("assistants.saveFailed", "Could not save"), {
        description: (e as Error).message,
      });
    } finally {
      setBusy(false);
    }
  }

  async function rotate() {
    setBusy(true);
    try {
      setNewKey(await editAssistant(assistant.appId, { rotateKey: true }));
    } catch (e) {
      toast.error(t("assistants.rotateFailed", "Could not rotate the key"), {
        description: (e as Error).message,
      });
    } finally {
      setBusy(false);
    }
  }

  return (
    <CardContent className="flex flex-col gap-4 border-t pt-4">
      <div className="flex flex-col gap-1.5">
        <label
          htmlFor={`name-${assistant.appId}`}
          className="text-sm font-medium"
        >
          {t("assistants.nameLabel", "Name")}
        </label>
        <Input
          id={`name-${assistant.appId}`}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </div>
      <AssistantGroupPicker levels={levels} onChange={setLevels} />
      <div className="flex flex-wrap gap-2">
        <Button disabled={busy || nothing || !name.trim()} onClick={save}>
          {t("assistants.save", "Save changes")}
        </Button>
        <Button variant="outline" disabled={busy} onClick={rotate}>
          {t("assistants.rotate", "Rotate key")}
        </Button>
      </div>
      {newKey && (
        <div className="flex flex-col gap-1.5">
          <span className="text-sm text-muted-foreground">
            {t(
              "assistants.newKeyNote",
              "New key — shown once. The old key has stopped working; paste this one into the assistant."
            )}
          </span>
          <Input readOnly value={newKey} onFocus={(e) => e.target.select()} />
        </div>
      )}
    </CardContent>
  );
}

const DEFAULT_LOCAL_PORT = 5607;

// LocalAccessCard is the desktop-only switch for serving assistants that run on
// this computer (127.0.0.1). Off by default; every request still needs the
// assistant's own connection key.
function LocalAccessCard({
  port,
  onApply,
}: {
  port: number;
  onApply: (port: number) => void;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = React.useState(String(port || DEFAULT_LOCAL_PORT));
  React.useEffect(() => {
    if (port) {
      setDraft(String(port));
    }
  }, [port]);
  const enabled = port > 0;
  const parsed = Number(draft);
  const valid = Number.isInteger(parsed) && parsed >= 1024 && parsed <= 65535;

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between gap-4">
        <div className="flex flex-col gap-1.5">
          <CardTitle className="flex items-center gap-2">
            <LaptopIcon className="w-4 h-4" />
            {t("assistants.localTitle", "Assistants on this computer")}
          </CardTitle>
          <CardDescription>
            {enabled
              ? t("assistants.localOn", {
                  defaultValue:
                    "Assistants on this computer can connect at http://127.0.0.1:{{port}}/mcp with their connection key.",
                  port,
                })
              : t(
                  "assistants.localOff",
                  "Off. Turn on to let an assistant running on this computer connect. Nothing outside this computer can reach it."
                )}
          </CardDescription>
        </div>
        <Switch
          checked={enabled}
          disabled={!enabled && !valid}
          aria-label={t("assistants.localTitle", "Assistants on this computer")}
          onCheckedChange={(v) => onApply(v ? parsed : 0)}
        />
      </CardHeader>
      {!enabled && (
        <CardContent className="flex items-center gap-3">
          <label
            htmlFor="local-port"
            className="text-sm text-muted-foreground shrink-0"
          >
            {t("assistants.localPort", "Port")}
          </label>
          <Input
            id="local-port"
            inputMode="numeric"
            className="max-w-32"
            value={draft}
            onChange={(e) => setDraft(e.target.value.replace(/[^0-9]/g, ""))}
            aria-invalid={!valid}
          />
          {!valid && (
            <span className="text-sm text-destructive">
              {t(
                "assistants.localPortInvalid",
                "Use a port from 1024 to 65535"
              )}
            </span>
          )}
        </CardContent>
      )}
    </Card>
  );
}

// PendingFrom shows WHO is asking in a way a lookalike name cannot fake: the
// assistant's id, when it was connected (flagged when new) and when it asked.
function PendingFrom({
  tool,
  summary,
  createdAt,
  appId,
  assistant,
}: {
  tool: string;
  summary: string;
  createdAt: string;
  appId: number;
  assistant?: AIAssistant;
}) {
  const { t } = useTranslation("apps");
  const isNew =
    assistant && dayjs(assistant.createdAt).isAfter(dayjs().subtract(1, "day"));
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <span className="font-medium break-words whitespace-pre-line">
        {summary}
      </span>
      <span className="text-xs text-muted-foreground">
        <code>{tool}</code>
      </span>
      <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted-foreground">
        <span>
          {assistant?.name ?? t("assistants.unknown", "An assistant")} · #
          {appId}
        </span>
        {assistant && (
          <span>
            {t("assistants.connectedAgo", {
              defaultValue: "connected {{ago}}",
              ago: dayjs(assistant.createdAt).fromNow(),
            })}
          </span>
        )}
        {isNew && (
          <Badge variant="warning">
            {t("assistants.newAssistant", "New assistant")}
          </Badge>
        )}
        <span>
          {t("assistants.askedAgo", {
            defaultValue: "asked {{ago}}",
            ago: dayjs(createdAt).fromNow(),
          })}
        </span>
      </span>
    </div>
  );
}

// ApprovalLimitCard sets the amount above which an assistant's payment or
// transfer waits for the owner's approval (decision D22).
function ApprovalLimitCard({
  limit,
  spent,
  onSave,
}: {
  limit: number;
  spent: number;
  onSave: (loki: number) => void;
}) {
  const { t } = useTranslation("apps");
  const [draft, setDraft] = React.useState(String(limit));
  React.useEffect(() => setDraft(String(limit)), [limit]);
  const parsed = Number(draft);
  const valid = draft !== "" && Number.isInteger(parsed) && parsed >= 0;
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <ShieldAlertIcon className="w-4 h-4" />
          {t("assistants.limitTitle", "Approval limit")}
        </CardTitle>
        <CardDescription>
          {t(
            "assistants.limitDescription",
            "The most your assistants may pay or transfer, together, in any 24 hours without asking you (each payment counts with its maximum routing fee). Beyond it, each payment waits for your approval here, even after the assistant confirms it. Trusting an LSP, creating cash, publishing your node profile, changing order terms and taking back money held for others always wait for you. Set 0 to approve every payment."
          )}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap items-center gap-3">
        <Input
          id="approval-limit"
          inputMode="numeric"
          className="max-w-40"
          value={draft}
          aria-label={t("assistants.limitTitle", "Approval limit")}
          onChange={(e) => setDraft(e.target.value.replace(/[^0-9]/g, ""))}
        />
        <span className="text-sm text-muted-foreground">loki</span>
        <span className="text-sm text-muted-foreground">
          {t("assistants.limitUsed", {
            defaultValue: "{{spent}} used in the last 24 hours",
            spent: spent.toLocaleString(),
          })}
        </span>
        <Button
          variant="outline"
          disabled={!valid || parsed === limit}
          onClick={() => onSave(parsed)}
        >
          {t("assistants.limitSave", "Save")}
        </Button>
      </CardContent>
    </Card>
  );
}
