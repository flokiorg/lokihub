import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import { CashHubConfigCard } from "src/components/CashHubConfigCard";
import { Button } from "src/components/ui/button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { createApp } from "src/requests/createApp";
import { CreateAppRequest } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";

export function NewCashHub() {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  const [name, setName] = React.useState("");
  const [perWalletMaxLoki, setPerWalletMaxLoki] = React.useState(1000);
  const [maxExpSecs, setMaxExpSecs] = React.useState(86400);
  const [minTransferLoki, setMinTransferLoki] = React.useState(0);
  const [redeemFeePpm, setRedeemFeePpm] = React.useState(0);
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

  return (
    <div className="grid gap-5">
      <AppHeader
        title={t("newCashHub.title")}
        description={t("newCashHub.description")}
      />
      <form onSubmit={handleSubmit} className="flex flex-col items-start gap-4 max-w-lg">
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
          budgetLabel={t("common.maxWalletBudgetLabel")}
          budgetHelper={t("newCashHub.maxWalletBudgetHelper")}
          expiryLabel={t("common.maxWalletExpiryLabel")}
          expiryHelper={t("newCashHub.maxExpiryHelper")}
          perWalletMaxLoki={perWalletMaxLoki}
          onPerWalletMaxLokiChange={setPerWalletMaxLoki}
          maxExpSecs={maxExpSecs}
          onMaxExpSecsChange={setMaxExpSecs}
          minTransferLoki={minTransferLoki}
          onMinTransferLokiChange={setMinTransferLoki}
          redeemFeePpm={redeemFeePpm}
          onRedeemFeePpmChange={setRedeemFeePpm}
        />
        <div className="flex gap-3">
          <Button type="button" variant="outline" onClick={() => navigate(-1)}>
            {tc("actions.cancel")}
          </Button>
          <LoadingButton loading={isLoading} type="submit">
            {t("newCashHub.submit")}
          </LoadingButton>
        </div>
      </form>
    </div>
  );
}
