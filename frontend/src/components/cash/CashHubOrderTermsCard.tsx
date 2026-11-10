import { PlusIcon, TrashIcon, TriangleAlertIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  ceilingExceedsOutbound,
  formFromSettings,
  type KindForm,
  MAX_FEE_PPM_HINT,
  parseTermsForm,
  type TermsForm,
  type TierForm,
} from "src/components/cash/orders";
import { useNativeAmountFormat } from "src/hooks/useNativeAmountFormat";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { Button } from "src/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "src/components/ui/card";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { Switch } from "src/components/ui/switch";
import { useBalances } from "src/hooks/useBalances";
import { useOrderTerms } from "src/hooks/useCashOrders";
import { lokiToMillis } from "src/components/withdraw/withdraw";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

// CashHubOrderTermsCard edits a Cash Hub's selling terms and open-order ceilings
// (NIP-CO / WP-19 item 8): per-kind Lightning and on-chain bounds, fees and
// window, on-chain confirmation tiers, the bill terms, and the open-order
// ceilings. Null terms are the "not selling yet" state. The PUT refuses any out
// of bounds (never clamps) and this surfaces its message. It warns when the
// on-chain ceiling exceeds the node's Lightning outbound capacity (H-CO-103).
export function CashHubOrderTermsCard({ hubId }: { hubId: number }) {
  const { t } = useTranslation("settings");
  const { loki } = useNativeAmountFormat();
  const { data: view, error, mutate } = useOrderTerms(hubId);
  const { data: balances } = useBalances();
  const [form, setForm] = useState<TermsForm | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (view !== undefined && !form) {
      setForm(formFromSettings(view));
    }
  }, [view, form]);

  if (error) {
    return null;
  }
  if (view === undefined || !form) {
    return null;
  }

  const parsed = parseTermsForm(form);
  const set = (patch: Partial<TermsForm>) => setForm({ ...form, ...patch });
  const setKind = (which: "lightning" | "onchain", patch: Partial<KindForm>) =>
    setForm({ ...form, [which]: { ...form[which], ...patch } });

  // H-CO-103: the node's Lightning outbound (spendable) capacity. totalSpendable
  // is already in millis (milli-loki), so it feeds the millis comparison directly.
  const outboundMillis = balances?.lightning.totalSpendable ?? 0;
  const ceilingOnchainMillis = lokiToMillis(form.ceilingOnchainLoki) ?? 0;
  const outboundWarn =
    form.onchain.enabled &&
    balances != null &&
    ceilingExceedsOutbound(ceilingOnchainMillis, outboundMillis);

  async function save() {
    if (!parsed.settings) {
      return;
    }
    setSaving(true);
    try {
      await request(`/api/cash-hubs/${hubId}/order-terms`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(parsed.settings),
      });
      const next = await mutate();
      setForm(formFromSettings(next ?? null));
      toast(t("sellCash.terms.saved"));
    } catch (e) {
      handleRequestError(t("sellCash.terms.saveFailed"), e);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("sellCash.terms.title")}</CardTitle>
        <CardDescription>
          {view
            ? t("sellCash.terms.selling")
            : t("sellCash.terms.notSellingYet")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-6">
        <KindFields
          which="lightning"
          hubId={hubId}
          form={form}
          onKind={(patch) => setKind("lightning", patch)}
          onCeiling={(ceilingLightningLoki) => set({ ceilingLightningLoki })}
        />
        <KindFields
          which="onchain"
          hubId={hubId}
          form={form}
          onKind={(patch) => setKind("onchain", patch)}
          onCeiling={(ceilingOnchainLoki) => set({ ceilingOnchainLoki })}
        />
        {form.onchain.enabled && (
          <OnchainTiers
            hubId={hubId}
            tiers={form.tiers}
            onTiers={(tiers) => set({ tiers })}
          />
        )}
        {outboundWarn && (
          <Alert variant="warning">
            <TriangleAlertIcon />
            <AlertTitle>{t("sellCash.terms.outboundWarnTitle")}</AlertTitle>
            <AlertDescription>
              {t("sellCash.terms.outboundWarn", {
                ceiling: loki(ceilingOnchainMillis),
                outbound: loki(outboundMillis),
              })}
            </AlertDescription>
          </Alert>
        )}
        <div className="grid gap-4">
          <h4 className="font-medium">{t("sellCash.terms.billTitle")}</h4>
          <div className="grid grid-cols-2 gap-4">
            <Field
              id={`order-redeem-base-${hubId}`}
              label={t("sellCash.terms.redeemFeeBase")}
              value={form.redeemFeeBaseLoki}
              onChange={(redeemFeeBaseLoki) => set({ redeemFeeBaseLoki })}
            />
            <Field
              id={`order-redeem-ppm-${hubId}`}
              label={t("sellCash.terms.redeemFeePpm")}
              value={form.redeemFeePpm}
              numeric
              onChange={(redeemFeePpm) => set({ redeemFeePpm })}
            />
          </div>
          <Field
            id={`order-min-transfer-${hubId}`}
            label={t("sellCash.terms.minTransfer")}
            value={form.minTransferLoki}
            onChange={(minTransferLoki) => set({ minTransferLoki })}
          />
        </div>
        <div className="grid gap-4">
          <h4 className="font-medium">{t("sellCash.terms.openTitle")}</h4>
          <Field
            id={`order-open-onchain-max-${hubId}`}
            label={t("sellCash.terms.openOnchainMax")}
            value={form.openOnchainMax}
            numeric
            onChange={(openOnchainMax) => set({ openOnchainMax })}
          />
        </div>
        {parsed.error && (
          <p className="text-sm text-destructive">
            {t(`sellCash.terms.errors.${parsed.error}`, {
              max: MAX_FEE_PPM_HINT,
            })}
          </p>
        )}
        <LoadingButton
          type="button"
          loading={saving}
          disabled={!parsed.settings}
          onClick={() => void save()}
        >
          {t("sellCash.terms.save")}
        </LoadingButton>
      </CardContent>
    </Card>
  );
}

function Field({
  id,
  label,
  value,
  numeric,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  numeric?: boolean;
  onChange: (v: string) => void;
}) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        inputMode={numeric ? "numeric" : "decimal"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </div>
  );
}

function KindFields({
  which,
  hubId,
  form,
  onKind,
  onCeiling,
}: {
  which: "lightning" | "onchain";
  hubId: number;
  form: TermsForm;
  onKind: (patch: Partial<KindForm>) => void;
  onCeiling: (v: string) => void;
}) {
  const { t } = useTranslation("settings");
  const kind = form[which];
  const ceiling =
    which === "lightning" ? form.ceilingLightningLoki : form.ceilingOnchainLoki;
  return (
    <div className="grid gap-4">
      <div className="flex items-center justify-between gap-4">
        <h4 className="font-medium">{t(`sellCash.terms.${which}Title`)}</h4>
        <Switch
          id={`order-${which}-enabled-${hubId}`}
          checked={kind.enabled}
          onCheckedChange={(enabled) => onKind({ enabled })}
        />
      </div>
      {kind.enabled && (
        <div className="grid gap-4">
          <div className="grid grid-cols-2 gap-4">
            <Field
              id={`order-${which}-min-${hubId}`}
              label={t("sellCash.terms.min")}
              value={kind.minLoki}
              onChange={(minLoki) => onKind({ minLoki })}
            />
            <Field
              id={`order-${which}-max-${hubId}`}
              label={t("sellCash.terms.max")}
              value={kind.maxLoki}
              onChange={(maxLoki) => onKind({ maxLoki })}
            />
          </div>
          <div className="grid grid-cols-2 gap-4">
            <Field
              id={`order-${which}-fee-base-${hubId}`}
              label={t("sellCash.terms.feeBase")}
              value={kind.feeBaseLoki}
              onChange={(feeBaseLoki) => onKind({ feeBaseLoki })}
            />
            <Field
              id={`order-${which}-fee-ppm-${hubId}`}
              label={t("sellCash.terms.feePpm")}
              value={kind.feePpm}
              numeric
              onChange={(feePpm) => onKind({ feePpm })}
            />
          </div>
          <div className="grid grid-cols-2 gap-4">
            <Field
              id={`order-${which}-window-${hubId}`}
              label={t("sellCash.terms.window")}
              value={kind.window}
              numeric
              onChange={(window) => onKind({ window })}
            />
            <Field
              id={`order-${which}-ceiling-${hubId}`}
              label={t("sellCash.terms.ceiling")}
              value={ceiling}
              onChange={onCeiling}
            />
          </div>
        </div>
      )}
    </div>
  );
}

function OnchainTiers({
  hubId,
  tiers,
  onTiers,
}: {
  hubId: number;
  tiers: TierForm[];
  onTiers: (tiers: TierForm[]) => void;
}) {
  const { t } = useTranslation("settings");
  const setTier = (i: number, patch: Partial<TierForm>) =>
    onTiers(tiers.map((tr, j) => (j === i ? { ...tr, ...patch } : tr)));
  return (
    <div className="grid gap-2">
      <h4 className="font-medium">{t("sellCash.terms.tiersTitle")}</h4>
      <p className="text-sm text-muted-foreground">
        {t("sellCash.terms.tiersHint")}
      </p>
      {tiers.map((tr, i) => {
        const last = i === tiers.length - 1;
        return (
          <div key={i} className="flex items-end gap-2">
            <div className="grid gap-1.5 flex-1">
              <Label htmlFor={`order-tier-up-${hubId}-${i}`}>
                {last ? t("sellCash.terms.tierOpen") : t("sellCash.terms.tierUpTo")}
              </Label>
              <Input
                id={`order-tier-up-${hubId}-${i}`}
                inputMode="decimal"
                disabled={last}
                placeholder={last ? t("sellCash.terms.tierOpen") : ""}
                value={tr.upToLoki}
                onChange={(e) => setTier(i, { upToLoki: e.target.value })}
              />
            </div>
            <div className="grid gap-1.5 w-28">
              <Label htmlFor={`order-tier-n-${hubId}-${i}`}>
                {t("sellCash.terms.tierN")}
              </Label>
              <Input
                id={`order-tier-n-${hubId}-${i}`}
                inputMode="numeric"
                value={tr.n}
                onChange={(e) => setTier(i, { n: e.target.value })}
              />
            </div>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              disabled={tiers.length === 1}
              onClick={() => onTiers(tiers.filter((_, j) => j !== i))}
              aria-label={t("sellCash.terms.tierRemove")}
            >
              <TrashIcon />
            </Button>
          </div>
        );
      })}
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="w-fit"
        onClick={() =>
          onTiers([
            ...tiers.slice(0, -1),
            { upToLoki: "", n: "1" },
            tiers[tiers.length - 1],
          ])
        }
      >
        <PlusIcon />
        {t("sellCash.terms.tierAdd")}
      </Button>
    </div>
  );
}
