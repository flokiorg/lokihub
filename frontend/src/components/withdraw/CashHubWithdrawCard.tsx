import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
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
import {
  formFromView,
  hubEnableBlocked,
  MAX_FEE_PPM,
  parseWithdrawForm,
  scriptTypeName,
  type WithdrawForm,
} from "src/components/withdraw/withdraw";
import { useHubWithdrawTerms } from "src/hooks/useCashWithdraw";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

// CashHubWithdrawCard sets one Cash Hub's on-chain withdrawal terms, the only
// operator inputs: its own enable, withdraw_fee_base, withdraw_fee_ppm and an
// optional minimum and maximum (NIP-CASH §Redeeming On-chain). Script types
// and final_depth are shown read-only (WP-19 item 10).
export function CashHubWithdrawCard({ hubId }: { hubId: number }) {
  const { t } = useTranslation("settings");
  const { data: view, error, mutate } = useHubWithdrawTerms(hubId);
  const [form, setForm] = useState<WithdrawForm | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (view && !form) {
      setForm(formFromView(view));
    }
  }, [view, form]);

  if (error) {
    // The node has no on-chain withdrawal service: nothing to set.
    return null;
  }
  if (!view || !form) {
    return null;
  }

  const blocked = hubEnableBlocked(view);
  const parsed = parseWithdrawForm(form);
  const set = (patch: Partial<WithdrawForm>) => setForm({ ...form, ...patch });

  async function save() {
    if (!parsed.input) {
      return;
    }
    setSaving(true);
    try {
      const next = await request<typeof view>(
        `/api/cash-hubs/${hubId}/withdraw-terms`,
        {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(parsed.input),
        }
      );
      if (next) {
        await mutate(next, false);
        setForm(formFromView(next));
      }
      toast(t("withdraw.saved"));
    } catch (e) {
      handleRequestError(t("withdraw.saveFailed"), e);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("withdraw.hubTitle")}</CardTitle>
        <CardDescription>
          {view.offered ? t("withdraw.offered") : t("withdraw.notOffered")}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <div className="flex items-center justify-between gap-4">
          <div className="grid gap-1">
            <Label htmlFor={`withdraw-enabled-${hubId}`}>
              {t("withdraw.hubEnable")}
            </Label>
            {blocked && (
              <p className="text-sm text-muted-foreground">
                {t(`withdraw.blocked.${blocked}`)}
              </p>
            )}
          </div>
          <Switch
            id={`withdraw-enabled-${hubId}`}
            checked={form.enabled}
            disabled={!!blocked && !form.enabled}
            onCheckedChange={(enabled) => set({ enabled })}
          />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor={`withdraw-fee-base-${hubId}`}>
            {t("withdraw.feeBase")}
          </Label>
          <Input
            id={`withdraw-fee-base-${hubId}`}
            inputMode="decimal"
            value={form.feeBaseLoki}
            onChange={(e) => set({ feeBaseLoki: e.target.value })}
          />
        </div>
        <div className="grid gap-1.5">
          <Label htmlFor={`withdraw-fee-ppm-${hubId}`}>
            {t("withdraw.feePpm")}
          </Label>
          <Input
            id={`withdraw-fee-ppm-${hubId}`}
            inputMode="numeric"
            value={form.feePpm}
            onChange={(e) => set({ feePpm: e.target.value })}
          />
        </div>
        <div className="grid grid-cols-2 gap-4">
          <div className="grid gap-1.5">
            <Label htmlFor={`withdraw-min-${hubId}`}>{t("withdraw.min")}</Label>
            <Input
              id={`withdraw-min-${hubId}`}
              inputMode="decimal"
              placeholder={t("withdraw.none")}
              value={form.minLoki}
              onChange={(e) => set({ minLoki: e.target.value })}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor={`withdraw-max-${hubId}`}>{t("withdraw.max")}</Label>
            <Input
              id={`withdraw-max-${hubId}`}
              inputMode="decimal"
              placeholder={t("withdraw.none")}
              value={form.maxLoki}
              onChange={(e) => set({ maxLoki: e.target.value })}
            />
          </div>
        </div>
        {parsed.error && (
          <p className="text-sm text-destructive">
            {t(`withdraw.errors.${parsed.error}`, { max: MAX_FEE_PPM })}
          </p>
        )}
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">{t("withdraw.scriptTypes")}</dt>
          <dd>{view.script_types.map(scriptTypeName).join(", ")}</dd>
          <dt className="text-muted-foreground">{t("withdraw.finalDepth")}</dt>
          <dd>{view.final_depth}</dd>
        </dl>
        <p className="text-sm text-muted-foreground">
          {t("withdraw.networkFeeNote")}
        </p>
        <LoadingButton
          type="button"
          loading={saving}
          disabled={!parsed.input}
          onClick={() => void save()}
        >
          {t("withdraw.save")}
        </LoadingButton>
      </CardContent>
    </Card>
  );
}
