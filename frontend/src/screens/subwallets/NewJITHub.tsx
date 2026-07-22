import React from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import { JITHubConfigCard } from "src/components/JITHubConfigCard";
import { Button } from "src/components/ui/button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { createApp } from "src/requests/createApp";
import { CreateAppRequest } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";

export function NewJITHub() {
  const { t } = useTranslation("circles");
  const { t: tc } = useTranslation("common");
  const navigate = useNavigate();
  const [name, setName] = React.useState("");
  const [perWalletMaxLoki, setPerWalletMaxLoki] = React.useState(1000);
  const [maxExpSecs, setMaxExpSecs] = React.useState(86400);
  const [isLoading, setLoading] = React.useState(false);

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setLoading(true);
    try {
      const req: CreateAppRequest = {
        name,
        kind: "jit_hub",
        scopes: [
          "jit_hub",
          "get_balance",
          "get_info",
          "list_transactions",
          "lookup_invoice",
          "make_invoice",
          "notifications",
          "pay_invoice",
        ],
        jitPerWalletMaxMloki: perWalletMaxLoki * 1000,
        jitMaxExpSecs: maxExpSecs,
        metadata: { app_store_app_id: SUBWALLET_APPSTORE_APP_ID },
      };
      const response = await createApp(req);
      navigate("/sub-wallets/created", { state: response });
      toast(t("newJitHub.createdToast", { name }));
    } catch (error) {
      handleRequestError(t("newJitHub.errors.create"), error);
    }
    setLoading(false);
  };

  return (
    <div className="grid gap-5">
      <AppHeader
        title={t("newJitHub.title")}
        description={t("newJitHub.description")}
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
        <JITHubConfigCard
          budgetLabel={t("common.maxWalletBudgetLabel")}
          budgetHelper={t("newJitHub.maxWalletBudgetHelper")}
          expiryLabel={t("common.maxWalletExpiryLabel")}
          expiryHelper={t("newJitHub.maxExpiryHelper")}
          perWalletMaxLoki={perWalletMaxLoki}
          onPerWalletMaxLokiChange={setPerWalletMaxLoki}
          maxExpSecs={maxExpSecs}
          onMaxExpSecsChange={setMaxExpSecs}
        />
        <div className="flex gap-3">
          <Button type="button" variant="outline" onClick={() => navigate(-1)}>
            {tc("actions.cancel")}
          </Button>
          <LoadingButton loading={isLoading} type="submit">
            {t("newJitHub.submit")}
          </LoadingButton>
        </div>
      </form>
    </div>
  );
}
