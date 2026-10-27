import { ChevronDown, ChevronRight, Network } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  sameTitle,
  shortNodeId,
  visibleCatalogue,
} from "src/components/lsps/catalogue";
import { Badge } from "src/components/ui/badge";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { Checkbox } from "src/components/ui/checkbox";
import { Input } from "src/components/ui/input";
import { useInfo } from "src/hooks/useInfo";
import {
  type LSPCatalogueEntry,
  addLSP,
  checkLSP,
  trustLSP,
  useLSPCatalogue,
} from "src/hooks/useLSPCatalogue";
import { handleRequestError } from "src/utils/handleRequestError";

const PROTOCOLS = [1, 2, 5];

// LSPCatalogue is Settings > Services' LSP list (WP-26): every LSP announced
// for this node's chain and network, unranked; the user ticks the ones they
// trust (LP-1, LP-12). Titles, descriptions and websites are the LSP's claims;
// whether the node is in this node's graph is this node's own fact.
export function LSPCatalogue({
  className,
  disabled,
}: {
  className?: string;
  disabled?: boolean;
}) {
  const { t } = useTranslation("setup");
  const { data, mutate } = useLSPCatalogue();
  const { mutate: reloadInfo } = useInfo();
  const [text, setText] = React.useState("");
  const [protocol, setProtocol] = React.useState<number | undefined>();
  const [showOthers, setShowOthers] = React.useState(false);
  const [adding, setAdding] = React.useState("");
  const [busy, setBusy] = React.useState<string | undefined>();

  if (disabled) {
    return (
      <Card className={className}>
        <CardHeader>
          <CardTitle className="text-lg flex items-center gap-2">
            <Network className="w-5 h-5 text-primary" />
            {t("services.lsp.title")}
          </CardTitle>
          <CardDescription>{t("services.lsp.setupNote")}</CardDescription>
        </CardHeader>
      </Card>
    );
  }

  const all = data ?? [];
  const { inGraph, notInGraph } = visibleCatalogue(all, { protocol, text });
  const dupes = sameTitle(all);

  const setTrusted = async (l: LSPCatalogueEntry, trusted: boolean) => {
    setBusy(l.nodeKey);
    try {
      const res = await trustLSP(l.nodeKey, trusted);
      if (trusted && res?.lastCheck?.error) {
        toast.warning(t("services.lsp.checkUnreachable"), {
          description: res.lastCheck.error,
        });
      }
      await Promise.all([mutate(), reloadInfo()]);
    } catch (error) {
      handleRequestError(t("services.lsp.trustError"), error);
    } finally {
      setBusy(undefined);
    }
  };

  const recheck = async (l: LSPCatalogueEntry) => {
    setBusy(l.nodeKey);
    try {
      await checkLSP(l.nodeKey);
      await mutate();
    } catch (error) {
      handleRequestError(t("services.lsp.checkUnreachable"), error);
    } finally {
      setBusy(undefined);
    }
  };

  const add = async () => {
    try {
      await addLSP(adding.trim());
      setAdding("");
      await mutate();
    } catch (error) {
      handleRequestError(t("services.lsp.notFound"), error);
    }
  };

  const row = (l: LSPCatalogueEntry) => (
    <LSPRow
      key={l.nodeKey}
      lsp={l}
      duplicate={!!l.name && dupes.has(l.name.toLowerCase())}
      busy={busy === l.nodeKey}
      onTrust={(v) => setTrusted(l, v)}
      onRecheck={() => recheck(l)}
    />
  );

  return (
    <Card className={className}>
      <CardHeader className="pb-3">
        <CardTitle className="text-lg flex items-center gap-2">
          <Network className="w-5 h-5 text-primary" />
          {t("services.lsp.title")}
        </CardTitle>
        <CardDescription>{t("services.lsp.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex flex-wrap gap-2">
          <Input
            className="max-w-xs"
            placeholder={t("services.lsp.filterText")}
            value={text}
            onChange={(e) => setText(e.target.value)}
          />
          <Button
            type="button"
            size="sm"
            variant={protocol === undefined ? "default" : "outline"}
            onClick={() => setProtocol(undefined)}
          >
            {t("services.lsp.allProtocols")}
          </Button>
          {PROTOCOLS.map((p) => (
            <Button
              key={p}
              type="button"
              size="sm"
              variant={protocol === p ? "default" : "outline"}
              onClick={() => setProtocol(p)}
            >
              LSPS{p}
            </Button>
          ))}
        </div>
        {inGraph.length === 0 && notInGraph.length === 0 && (
          <p className="text-sm text-muted-foreground">
            {t("services.lsp.empty")}
          </p>
        )}
        <div className="space-y-2" data-testid="lsp-catalogue">
          {inGraph.map(row)}
        </div>
        {notInGraph.length > 0 && (
          <div className="space-y-2">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setShowOthers(!showOthers)}
            >
              {showOthers ? <ChevronDown /> : <ChevronRight />}
              {t("services.lsp.notInGraph", { count: notInGraph.length })}
            </Button>
            {showOthers && notInGraph.map(row)}
          </div>
        )}
        <div className="flex gap-2">
          <Input
            placeholder={t("services.lsp.addPlaceholder")}
            value={adding}
            onChange={(e) => setAdding(e.target.value)}
          />
          <Button type="button" onClick={add} disabled={!adding.trim()}>
            {t("services.lsp.add")}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          {t("services.lsp.claimsNote")}
        </p>
      </CardContent>
    </Card>
  );
}

function LSPRow({
  lsp,
  duplicate,
  busy,
  onTrust,
  onRecheck,
}: {
  lsp: LSPCatalogueEntry;
  duplicate: boolean;
  busy: boolean;
  onTrust: (trusted: boolean) => void;
  onRecheck: () => void;
}) {
  const { t } = useTranslation("setup");
  const [open, setOpen] = React.useState(false);
  const [showImage, setShowImage] = React.useState(false);
  const fee = lsp.lsps2Fees?.[0];
  const check = lsp.lastCheck;
  return (
    <div className="rounded-md border p-3 text-sm" data-testid="lsp-row">
      <div className="flex items-start gap-3">
        <Checkbox
          checked={lsp.trusted}
          disabled={busy}
          aria-label={t("services.lsp.trust")}
          onCheckedChange={(v) => onTrust(v === true)}
        />
        <div className="flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">
              {lsp.name || t("services.lsp.untitled")}
            </span>
            <span className="font-mono text-xs text-muted-foreground">
              {shortNodeId(lsp.nodeKey)}
            </span>
            {duplicate && (
              <Badge variant="destructive">{t("services.lsp.sameTitle")}</Badge>
            )}
            {lsp.status && lsp.status !== "open" && (
              <Badge variant="outline">
                {t(`services.lsp.status.${lsp.status}`)}
              </Badge>
            )}
            {!lsp.usable && (
              <Badge variant="outline">{t("services.lsp.notAnnounced")}</Badge>
            )}
            {(lsp.protocols ?? []).map((p) => (
              <Badge key={p} variant="secondary">
                LSPS{p}
              </Badge>
            ))}
          </div>
          {lsp.website && (
            <p className="text-xs text-muted-foreground break-all">
              {t("services.lsp.claimedWebsite")}: {lsp.website}
            </p>
          )}
          {fee && (
            <p className="text-xs text-muted-foreground">
              {t("services.lsp.feeHint", {
                min: Math.round(fee.MinFeeMillis / 1000),
                ppm: fee.Proportional,
              })}
            </p>
          )}
          <p className="text-xs text-muted-foreground">
            {lsp.inGraph
              ? t("services.lsp.inGraph", {
                  channels: lsp.graphChannels,
                  capacity: lsp.graphCapacity,
                })
              : t("services.lsp.notInGraphFact")}
          </p>
          {check && (
            <p
              className={
                check.error || !check.usable || check.worse
                  ? "text-xs text-destructive"
                  : "text-xs text-muted-foreground"
              }
              data-testid="lsp-check"
            >
              {check.error
                ? t("services.lsp.checkUnreachable")
                : !check.usable
                  ? t("services.lsp.checkMissing")
                  : check.worse
                    ? t("services.lsp.checkWorse")
                    : t("services.lsp.checkOk")}
            </p>
          )}
          <Button
            type="button"
            variant="link"
            size="sm"
            className="px-0"
            onClick={() => setOpen(!open)}
          >
            {t("services.lsp.details")}
          </Button>
          {open && (
            <div className="space-y-1 text-xs">
              {lsp.description && (
                <p className="whitespace-pre-wrap">{lsp.description}</p>
              )}
              <p className="font-mono break-all">{lsp.nodeKey}</p>
              {(lsp.addrs ?? []).map((a) => (
                <p key={a} className="font-mono">
                  {a}
                </p>
              ))}
              {(lsp.lsps1 ?? []).map((o) => (
                <p key={o.Name}>
                  lsps1 {o.Name}: {o.Value}
                </p>
              ))}
              {(lsp.lsps2Fees ?? []).map((r, i) => (
                <p key={i}>
                  lsps2 {r.MinFeeMillis} / {r.Proportional} ppm /{" "}
                  {r.MinLifetime} / {r.MaxClientToSelfDelay} /{" "}
                  {r.MinPaymentSizeMillis}–{r.MaxPaymentSizeMillis}
                </p>
              ))}
              {(lsp.lsps5 ?? []).map((o) => (
                <p key={o.Name}>
                  lsps5 {o.Name}: {o.Value}
                </p>
              ))}
              {(check?.changed ?? []).map((d) => (
                <p key={d.Field}>
                  {d.Field}: {d.Announced} → {d.Live}
                </p>
              ))}
              {lsp.image &&
                (showImage ? (
                  <img src={lsp.image} alt="" className="h-12" />
                ) : (
                  <Button
                    type="button"
                    variant="link"
                    size="sm"
                    className="px-0"
                    onClick={() => setShowImage(true)}
                  >
                    {t("services.lsp.showImage")}
                  </Button>
                ))}
              {lsp.trusted && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={onRecheck}
                >
                  {t("services.lsp.recheck")}
                </Button>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
