import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { statusRows } from "src/components/withdraw/withdraw";
import { Label } from "src/components/ui/label";
import { Switch } from "src/components/ui/switch";
import { useWithdrawNode, useWithdrawStatus } from "src/hooks/useCashWithdraw";
import { cn } from "src/lib/utils";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

// WithdrawNodeSettings is the node's "On-chain withdrawals" switch, refused
// on while the node cannot pay on-chain, with the payout engine's status
// (WP-19 item 10).
export function WithdrawNodeSettings() {
  const { t } = useTranslation("settings");
  const { data: node, error, mutate } = useWithdrawNode();
  const { data: status, mutate: reloadStatus } = useWithdrawStatus(
    !!node?.capable
  );
  const [saving, setSaving] = useState(false);

  async function setEnabled(enabled: boolean) {
    setSaving(true);
    try {
      await request("/api/cash-withdraw/node", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled }),
      });
      await Promise.all([mutate(), reloadStatus()]);
      toast(enabled ? t("withdraw.turnedOn") : t("withdraw.turnedOff"));
    } catch (e) {
      handleRequestError(t("withdraw.switchFailed"), e);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4">
        <div className="grid gap-1">
          <Label htmlFor="cash-withdraw-node">{t("withdraw.switch")}</Label>
          <p className="text-sm text-muted-foreground">
            {t("withdraw.switchDescription")}
          </p>
          {(error || (node && !node.capable)) && (
            <p className="text-sm text-muted-foreground">
              {t("withdraw.notCapable")}
            </p>
          )}
        </div>
        <Switch
          id="cash-withdraw-node"
          checked={!!node?.enabled}
          disabled={!node || saving || (!node.capable && !node.enabled)}
          onCheckedChange={(v) => void setEnabled(v)}
        />
      </div>
      {status && (
        <div className="rounded-md border p-3 text-sm">
          <p className="font-medium mb-2">
            {status.ready ? t("withdraw.ready") : t("withdraw.notReady")}
          </p>
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
            {statusRows(status).map((r) => (
              <div key={r.key} className="contents">
                <dt className="text-muted-foreground">
                  {t(`withdraw.status.${r.key}`)}
                </dt>
                <dd className={cn(r.alert && "text-destructive font-medium")}>
                  {r.value}
                </dd>
              </div>
            ))}
          </dl>
        </div>
      )}
    </div>
  );
}
