import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "src/components/ui/select";
import { Skeleton } from "src/components/ui/skeleton";
import {
  AnnounceAction,
  recheckLspAnnouncement,
  setLspAnnounceState,
  updateLspConfig,
  useLspAnnounceState,
  useLspConfig,
  useLspNodes,
} from "src/hooks/useLsp";
import { LspUnavailable } from "src/screens/lsp/LspUnavailable";
import { LSP_CONFIG_SECRET_KEYS, LspConfigObject } from "src/screens/lsp/types";
import { handleRequestError } from "src/utils/handleRequestError";

// The only fields edited from the Hub: the business fee knobs. They are safe to
// change live and arrive as strings (the NodeConfig json tags are ",string").
// Everything else in the config is shown read-only, and secret keys are never
// rendered (they are stripped by lspd — handlers.go configSecretFields). Labels
// live in the "lsp" i18n namespace under config.fields.<key>.
const EDITABLE_FEE_FIELDS = [
  "feeRate",
  "baseFeeMloki",
  "channelFeePermyriad",
  "channelMinimumFeeMloki",
] as const;

export function LspConfig() {
  const { t } = useTranslation("lsp");
  const { data: nodesResp, error: nodesError } = useLspNodes();
  const nodes = React.useMemo(() => nodesResp?.nodes ?? [], [nodesResp]);
  const [selected, setSelected] = React.useState<string | undefined>();

  React.useEffect(() => {
    if (!selected && nodes.length > 0) {
      setSelected(nodes[0].node_id);
    }
  }, [nodes, selected]);

  return (
    <div className="space-y-4">
      <AppHeader
        title={t("config.title")}
        description={t("config.description")}
      />

      {nodesError ? (
        <LspUnavailable error={nodesError} />
      ) : !nodesResp ? (
        <Skeleton className="h-24 w-full" />
      ) : nodes.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">
          {t("config.noNodes")}
        </p>
      ) : (
        <>
          {nodes.length > 1 && (
            <div className="flex items-center gap-2">
              <Label className="text-sm">{t("config.nodeLabel")}</Label>
              <Select value={selected} onValueChange={setSelected}>
                <SelectTrigger className="w-80">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {nodes.map((n) => (
                    <SelectItem key={n.node_id} value={n.node_id}>
                      {n.name || n.node_id.slice(0, 16) + "…"}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          {selected && <ConfigNode node={selected} />}
        </>
      )}
    </div>
  );
}

function ConfigNode({ node }: { node: string }) {
  const { data, error, mutate } = useLspConfig(node);

  if (error) {
    return <LspUnavailable error={error} />;
  }
  if (!data) {
    return <Skeleton className="h-64 w-full" />;
  }

  return (
    <div className="space-y-4">
      <FeeEditor config={data.config} node={node} onSaved={() => mutate()} />
      <ConfigViewer config={data.config} relays={data.announce_relays} />
      <AnnouncePanel node={node} />
    </div>
  );
}

function FeeEditor({
  config,
  node,
  onSaved,
}: {
  config: LspConfigObject;
  node: string;
  onSaved: () => void;
}) {
  const { t } = useTranslation("lsp");
  const initial = React.useMemo(() => {
    const m: Record<string, string> = {};
    for (const f of EDITABLE_FEE_FIELDS) {
      const v = config[f];
      m[f] = v === undefined || v === null ? "" : String(v);
    }
    return m;
  }, [config]);

  const [form, setForm] = React.useState(initial);
  const [saving, setSaving] = React.useState(false);
  React.useEffect(() => setForm(initial), [initial]);

  const dirty = EDITABLE_FEE_FIELDS.some((f) => form[f] !== initial[f]);

  const fieldLabel = (key: (typeof EDITABLE_FEE_FIELDS)[number]): string => {
    switch (key) {
      case "feeRate":
        return t("config.fields.feeRate");
      case "baseFeeMloki":
        return t("config.fields.baseFeeMloki");
      case "channelFeePermyriad":
        return t("config.fields.channelFeePermyriad");
      case "channelMinimumFeeMloki":
        return t("config.fields.channelMinimumFeeMloki");
    }
  };

  const save = async () => {
    setSaving(true);
    try {
      // Send the full (secret-stripped) config back with only the fee fields
      // overwritten; lspd re-attaches secrets and validates the whole config.
      const next: LspConfigObject = { ...config };
      for (const f of EDITABLE_FEE_FIELDS) {
        next[f] = form[f];
      }
      await updateLspConfig(next, node);
      toast.success(t("config.fees.saved"));
      onSaved();
    } catch (error) {
      handleRequestError(t("config.fees.saveError"), error);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">{t("config.fees.title")}</CardTitle>
        <CardDescription>{t("config.fees.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          {EDITABLE_FEE_FIELDS.map((f) => (
            <div key={f} className="grid gap-1.5">
              <Label htmlFor={f}>{fieldLabel(f)}</Label>
              <Input
                id={f}
                inputMode="decimal"
                value={form[f] ?? ""}
                onChange={(e) =>
                  setForm((s) => ({ ...s, [f]: e.target.value }))
                }
              />
            </div>
          ))}
        </div>
        <div className="flex justify-end">
          <Button onClick={save} disabled={!dirty || saving}>
            {saving ? t("config.fees.saving") : t("config.fees.save")}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function ConfigViewer({
  config,
  relays,
}: {
  config: LspConfigObject;
  relays: string[];
}) {
  const { t } = useTranslation("lsp");
  const scalars: [string, string][] = [];
  const objects: [string, unknown][] = [];
  const editable = new Set<string>(EDITABLE_FEE_FIELDS);

  for (const [key, value] of Object.entries(config)) {
    if (LSP_CONFIG_SECRET_KEYS.has(key) || editable.has(key)) {
      continue;
    }
    if (value !== null && typeof value === "object") {
      objects.push([key, value]);
    } else {
      scalars.push([key, value === null ? "—" : String(value)]);
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">{t("config.viewer.title")}</CardTitle>
        <CardDescription>{t("config.viewer.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-2 lg:grid-cols-3">
          {scalars.map(([k, v]) => (
            <div key={k} className="grid gap-0.5">
              <span className="text-xs text-muted-foreground">{k}</span>
              <span className="break-all slashed-zero">{v}</span>
            </div>
          ))}
        </div>
        {objects.map(([k, v]) => (
          <div key={k} className="grid gap-1">
            <span className="text-xs text-muted-foreground">{k}</span>
            <pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs">
              {JSON.stringify(v, null, 2)}
            </pre>
          </div>
        ))}
        <div className="grid gap-1">
          <span className="text-xs text-muted-foreground">
            {t("config.viewer.announceRelays")}
          </span>
          {relays.length === 0 ? (
            <span className="text-sm text-muted-foreground">
              {t("values.none")}
            </span>
          ) : (
            <ul className="text-sm">
              {relays.map((r) => (
                <li key={r} className="font-mono text-xs break-all">
                  {r}
                </li>
              ))}
            </ul>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

function AnnouncePanel({ node }: { node: string }) {
  const { t } = useTranslation("lsp");
  const { data, error, mutate } = useLspAnnounceState(node);
  const [busy, setBusy] = React.useState(false);

  const act = async (
    fn: () => Promise<unknown>,
    success: string,
    failure: string
  ) => {
    setBusy(true);
    try {
      await fn();
      toast.success(success);
      await mutate();
    } catch (err) {
      handleRequestError(failure, err);
    } finally {
      setBusy(false);
    }
  };

  const actionToasts: Record<
    AnnounceAction,
    { success: string; failure: string }
  > = {
    pause: {
      success: t("config.announce.toast.pauseSuccess"),
      failure: t("config.announce.toast.pauseFailure"),
    },
    resume: {
      success: t("config.announce.toast.resumeSuccess"),
      failure: t("config.announce.toast.resumeFailure"),
    },
    unlist: {
      success: t("config.announce.toast.unlistSuccess"),
      failure: t("config.announce.toast.unlistFailure"),
    },
  };

  const doAction = (action: AnnounceAction) =>
    act(
      () => setLspAnnounceState(action, node),
      actionToasts[action].success,
      actionToasts[action].failure
    );

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base flex items-center gap-2">
          {t("config.announce.title")}
          {data && (
            <Badge
              variant={
                data.status === "published" || data.status === "active"
                  ? "positive"
                  : data.status === "paused"
                    ? "warning"
                    : "outline"
              }
            >
              {data.status}
            </Badge>
          )}
        </CardTitle>
        <CardDescription>{t("config.announce.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {error ? (
          <LspUnavailable error={error} />
        ) : !data ? (
          <Skeleton className="h-24 w-full" />
        ) : (
          <>
            <div className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
              <div className="grid gap-0.5">
                <span className="text-xs text-muted-foreground">
                  {t("config.announce.enabled")}
                </span>
                <span>{data.enabled ? t("values.yes") : t("values.no")}</span>
              </div>
              <div className="grid gap-0.5">
                <span className="text-xs text-muted-foreground">
                  {t("config.announce.lastChange")}
                </span>
                <span className="slashed-zero">
                  {data.updated_at
                    ? new Date(data.updated_at).toLocaleString()
                    : "—"}
                </span>
              </div>
            </div>

            {data.publisher && (
              <div className="space-y-2 text-sm">
                {data.publisher.blocked && (
                  <p className="text-destructive">
                    {t("config.announce.blocked", {
                      reason: data.publisher.blocked,
                    })}
                  </p>
                )}
                {data.publisher.last_error && (
                  <p className="text-destructive">
                    {data.publisher.last_error}
                  </p>
                )}
                {data.publisher.self_check && (
                  <div className="grid gap-0.5">
                    <span className="text-xs text-muted-foreground">
                      {t("config.announce.selfCheck")}
                    </span>
                    <span>
                      {data.publisher.self_check.ok
                        ? t("values.ok")
                        : t("config.announce.failed", {
                            problems:
                              data.publisher.self_check.problems.join(", "),
                          })}
                    </span>
                  </div>
                )}
                {data.publisher.last_event && (
                  <div className="grid gap-1">
                    <span className="text-xs text-muted-foreground">
                      {t("config.announce.relays")}
                    </span>
                    <ul className="text-xs">
                      {data.publisher.last_event.relays.map((r) => (
                        <li key={r.relay} className="font-mono break-all">
                          {r.accepted ? "✓" : "✗"} {r.relay}
                          {r.error ? ` — ${r.error}` : ""}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
              </div>
            )}

            <div className="flex flex-wrap gap-2">
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => doAction("pause")}
              >
                {t("config.announce.pause")}
              </Button>
              <Button
                size="sm"
                disabled={busy}
                onClick={() => doAction("resume")}
              >
                {t("config.announce.resume")}
              </Button>
              <Button
                size="sm"
                variant="destructive"
                disabled={busy}
                onClick={() => doAction("unlist")}
              >
                {t("config.announce.unlist")}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={() =>
                  act(
                    () => recheckLspAnnouncement(node),
                    t("config.announce.toast.recheckSuccess"),
                    t("config.announce.toast.recheckFailure")
                  )
                }
              >
                {t("config.announce.recheck")}
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
