import React from "react";
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
// rendered (they are stripped by lspd — handlers.go configSecretFields).
const EDITABLE_FEE_FIELDS: { key: string; label: string }[] = [
  { key: "feeRate", label: "Fee rate (proportional)" },
  { key: "baseFeeMloki", label: "Base fee (mloki)" },
  { key: "channelFeePermyriad", label: "Channel fee (permyriad)" },
  { key: "channelMinimumFeeMloki", label: "Channel minimum fee (mloki)" },
];

export function LspConfig() {
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
        title="Config"
        description="Node configuration and NIP-LSP announcement"
      />

      {nodesError ? (
        <LspUnavailable error={nodesError} />
      ) : !nodesResp ? (
        <Skeleton className="h-24 w-full" />
      ) : nodes.length === 0 ? (
        <p className="py-8 text-center text-sm text-muted-foreground">
          No nodes configured on the LSP daemon.
        </p>
      ) : (
        <>
          {nodes.length > 1 && (
            <div className="flex items-center gap-2">
              <Label className="text-sm">Node</Label>
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
  const initial = React.useMemo(() => {
    const m: Record<string, string> = {};
    for (const f of EDITABLE_FEE_FIELDS) {
      const v = config[f.key];
      m[f.key] = v === undefined || v === null ? "" : String(v);
    }
    return m;
  }, [config]);

  const [form, setForm] = React.useState(initial);
  const [saving, setSaving] = React.useState(false);
  React.useEffect(() => setForm(initial), [initial]);

  const dirty = EDITABLE_FEE_FIELDS.some((f) => form[f.key] !== initial[f.key]);

  const save = async () => {
    setSaving(true);
    try {
      // Send the full (secret-stripped) config back with only the fee fields
      // overwritten; lspd re-attaches secrets and validates the whole config.
      const next: LspConfigObject = { ...config };
      for (const f of EDITABLE_FEE_FIELDS) {
        next[f.key] = form[f.key];
      }
      await updateLspConfig(next, node);
      toast.success("Configuration saved");
      onSaved();
    } catch (error) {
      handleRequestError("Failed to save configuration", error);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base">Fees</CardTitle>
        <CardDescription>
          Business fee parameters for this node. Secret fields (node key,
          tokens, LND credentials) are never sent to the browser and are
          preserved on save.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          {EDITABLE_FEE_FIELDS.map((f) => (
            <div key={f.key} className="grid gap-1.5">
              <Label htmlFor={f.key}>{f.label}</Label>
              <Input
                id={f.key}
                inputMode="decimal"
                value={form[f.key] ?? ""}
                onChange={(e) =>
                  setForm((s) => ({ ...s, [f.key]: e.target.value }))
                }
              />
            </div>
          ))}
        </div>
        <div className="flex justify-end">
          <Button onClick={save} disabled={!dirty || saving}>
            {saving ? "Saving…" : "Save fees"}
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
  const scalars: [string, string][] = [];
  const objects: [string, unknown][] = [];
  const editable = new Set(EDITABLE_FEE_FIELDS.map((f) => f.key));

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
        <CardTitle className="text-base">Node configuration</CardTitle>
        <CardDescription>
          Read-only. Secrets are stripped by the daemon.
        </CardDescription>
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
          <span className="text-xs text-muted-foreground">announce relays</span>
          {relays.length === 0 ? (
            <span className="text-sm text-muted-foreground">none</span>
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
  const { data, error, mutate } = useLspAnnounceState(node);
  const [busy, setBusy] = React.useState(false);

  const act = async (fn: () => Promise<unknown>, label: string) => {
    setBusy(true);
    try {
      await fn();
      toast.success(label);
      await mutate();
    } catch (err) {
      handleRequestError(label + " failed", err);
    } finally {
      setBusy(false);
    }
  };

  const doAction = (action: AnnounceAction) =>
    act(() => setLspAnnounceState(action, node), `Announcement ${action}`);

  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="text-base flex items-center gap-2">
          NIP-LSP announcement
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
        <CardDescription>
          Pause, resume or unlist this node&apos;s kind-10735 announcement, and
          clear a failed self-check.
        </CardDescription>
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
                <span className="text-xs text-muted-foreground">Enabled</span>
                <span>{data.enabled ? "yes" : "no"}</span>
              </div>
              <div className="grid gap-0.5">
                <span className="text-xs text-muted-foreground">
                  Last change
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
                    Blocked: {data.publisher.blocked}
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
                      Self-check
                    </span>
                    <span>
                      {data.publisher.self_check.ok
                        ? "ok"
                        : "failed: " +
                          data.publisher.self_check.problems.join(", ")}
                    </span>
                  </div>
                )}
                {data.publisher.last_event && (
                  <div className="grid gap-1">
                    <span className="text-xs text-muted-foreground">
                      Relays
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
                Pause
              </Button>
              <Button
                size="sm"
                disabled={busy}
                onClick={() => doAction("resume")}
              >
                Resume
              </Button>
              <Button
                size="sm"
                variant="destructive"
                disabled={busy}
                onClick={() => doAction("unlist")}
              >
                Unlist
              </Button>
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={() =>
                  act(() => recheckLspAnnouncement(node), "Recheck")
                }
              >
                Recheck
              </Button>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}
