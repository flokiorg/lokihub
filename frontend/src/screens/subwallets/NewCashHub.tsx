import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import { CashHubConfigCard } from "src/components/CashHubConfigCard";
import { FormattedFlokicoinAmount } from "src/components/FormattedFlokicoinAmount";
import {
  DEFAULT_CASH_MIN_TRANSFER_LOKI,
  DEFAULT_CASH_SPENT_RETENTION_SECS,
} from "src/constants";
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
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { createApp } from "src/requests/createApp";
import { CreateAppRequest } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";

// Compact friendly duration for the summary ("1d", "6h", "30m").
function humanSecs(secs: number): string {
  if (secs % 86400 === 0) {
    return `${secs / 86400}d`;
  }
  if (secs % 3600 === 0) {
    return `${secs / 3600}h`;
  }
  if (secs % 60 === 0) {
    return `${secs / 60}m`;
  }
  return `${secs}s`;
}

export function NewCashHub() {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  const [name, setName] = React.useState("");
  const [perWalletMaxLoki, setPerWalletMaxLoki] = React.useState(1000);
  const [maxExpSecs, setMaxExpSecs] = React.useState(86400);
  const [minTransferLoki, setMinTransferLoki] = React.useState(
    DEFAULT_CASH_MIN_TRANSFER_LOKI
  );
  const [redeemFeePpm, setRedeemFeePpm] = React.useState(0);
  // Seeded with the backend's own default so the form shows what a hub would
  // get anyway, rather than looking unset.
  const [spentRetentionSecs, setSpentRetentionSecs] = React.useState(
    DEFAULT_CASH_SPENT_RETENTION_SECS
  );
  const [isLoading, setLoading] = React.useState(false);

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setLoading(true);
    try {
      const req: CreateAppRequest = {
        name,
        kind: "cash_hub",
        scopes: [
          "cash_hub",
          "get_balance",
          "get_info",
          "list_transactions",
          "lookup_invoice",
          "make_invoice",
          "notifications",
          "pay_invoice",
        ],
        cashPerWalletMaxMloki: perWalletMaxLoki * 1000,
        cashMaxExpSecs: maxExpSecs,
        cashMinTransferMloki: minTransferLoki * 1000,
        cashRedeemFeePpm: redeemFeePpm,
        cashSpentRetentionSecs: spentRetentionSecs,
        metadata: { app_store_app_id: SUBWALLET_APPSTORE_APP_ID },
      };
      const response = await createApp(req);
      navigate("/cash-hub/created", { state: response });
      toast(t("newCashHub.createdToast", { name }));
    } catch (error) {
      handleRequestError(t("newCashHub.errors.create"), error);
    }
    setLoading(false);
  };

  // Single submit-gate: a Cash Hub needs a name and a positive per-bill cap
  // (0 budget has no meaning). maxExpSecs=0 ("never") and the economics knobs
  // are all valid, so they do not gate submission.
  const canSubmit = name.trim().length > 0 && perWalletMaxLoki > 0;

  return (
    <div className="grid gap-5">
      <AppHeader
        title={t("newCashHub.title")}
        description={t("newCashHub.description")}
      />
      <form
        onSubmit={handleSubmit}
        className="grid w-full max-w-6xl gap-6 lg:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)] lg:items-start"
      >
        <div className="grid gap-4">
          <Card className="w-full">
            <CardHeader>
              <CardTitle>{t("newCashHub.essentialsTitle")}</CardTitle>
              <CardDescription>
                {t("newCashHub.essentialsDesc")}
              </CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4">
              <div className="w-full grid gap-1.5">
                <Label htmlFor="name">{t("common.nameLabel")}</Label>
                <Input
                  autoFocus
                  id="name"
                  type="text"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  required
                  autoComplete="off"
                />
              </div>
              <CashHubConfigCard
                budgetLabel={t("common.cashMaxBudgetLabel")}
                budgetHelper={t("newCashHub.maxWalletBudgetHelper")}
                expiryLabel={t("common.cashMaxExpiryLabel")}
                expiryHelper={t("newCashHub.maxExpiryHelper")}
                perWalletMaxLoki={perWalletMaxLoki}
                onPerWalletMaxLokiChange={setPerWalletMaxLoki}
                maxExpSecs={maxExpSecs}
                onMaxExpSecsChange={setMaxExpSecs}
                minTransferLoki={minTransferLoki}
                onMinTransferLokiChange={setMinTransferLoki}
                redeemFeePpm={redeemFeePpm}
                onRedeemFeePpmChange={setRedeemFeePpm}
                spentRetentionSecs={spentRetentionSecs}
                onSpentRetentionSecsChange={setSpentRetentionSecs}
              />
            </CardContent>
          </Card>
          <div className="grid gap-2">
            <div className="flex gap-3">
              <Button
                type="button"
                variant="outline"
                onClick={() => navigate(-1)}
              >
                {tc("actions.cancel")}
              </Button>
              <LoadingButton
                loading={isLoading}
                type="submit"
                disabled={!canSubmit}
              >
                {t("newCashHub.submit")}
              </LoadingButton>
            </div>
            {!canSubmit && (
              <p className="text-muted-foreground text-sm">
                {t("newCashHub.submitHint")}
              </p>
            )}
          </div>
        </div>

        {/* A live recap on the right fills the width and shows what this hub
            will do before it's created. */}
        <aside className="slashed-zero lg:sticky lg:top-4">
          <div className="rounded-xl border bg-muted/30 p-4">
            <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              {t("newCashHub.summaryTitle", "Summary")}
            </p>
            <div className="mt-3 grid gap-3 text-sm">
              <div>
                <p className="text-xs text-muted-foreground">
                  {t("common.nameLabel")}
                </p>
                <p className="font-medium">
                  {name || t("newCashHub.summaryName", "Untitled Cash Hub")}
                </p>
              </div>
              <div>
                <p className="text-xs text-muted-foreground">
                  {t("common.cashMaxBudgetLabel")}
                </p>
                <p className="balance sensitive font-medium">
                  <FormattedFlokicoinAmount amount={perWalletMaxLoki * 1000} />
                </p>
              </div>
              <div>
                <p className="text-xs text-muted-foreground">
                  {t("common.cashMaxExpiryLabel")}
                </p>
                <p className="font-medium">
                  {maxExpSecs === 0
                    ? t("newCashHub.summaryNoExpiry", "Never expires")
                    : humanSecs(maxExpSecs)}
                </p>
              </div>
              {redeemFeePpm > 0 && (
                <div>
                  <p className="text-xs text-muted-foreground">
                    {t("circleHub.cashRedeemFeeLabel", "Redeem fee")}
                  </p>
                  <p className="font-medium">{redeemFeePpm} ppm</p>
                </div>
              )}
            </div>
          </div>
        </aside>
      </form>
    </div>
  );
}
