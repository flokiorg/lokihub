import React from "react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import { Button } from "src/components/ui/button";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { createApp } from "src/requests/createApp";
import { CreateAppRequest } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";

export function NewSimpleSubwallet() {
  const navigate = useNavigate();
  const { t } = useTranslation("wallet");
  const [name, setName] = React.useState("");
  const [isLoading, setLoading] = React.useState(false);

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setLoading(true);

    try {
      const createAppRequest: CreateAppRequest = {
        name,
        scopes: [
          "get_balance",
          "get_info",
          "list_transactions",
          "lookup_invoice",
          "make_invoice",
          "notifications",
          "pay_invoice",
        ],
        kind: "isolated",
        metadata: {
          app_store_app_id: SUBWALLET_APPSTORE_APP_ID,
        },
      };

      const createAppResponse = await createApp(createAppRequest);

      navigate("/sub-wallets/created", {
        state: createAppResponse,
      });

      toast(t("subwallets.create.toast", { name }));
    } catch (error) {
      handleRequestError("Failed to create app", error);
    }
    setLoading(false);
  };

  return (
    <div className="grid gap-5">
      <AppHeader title={t("subwallets.create.title")} />
      <form
        onSubmit={handleSubmit}
        className="flex flex-col items-start gap-3 max-w-lg"
      >
        <div className="w-full grid gap-1.5 mb-4">
          <Label htmlFor="name">{t("subwallets.create.nameLabel")}</Label>
          <Input
            autoFocus
            type="text"
            name="name"
            value={name}
            id="name"
            onChange={(e) => setName(e.target.value)}
            required
            autoComplete="off"
          />
          <p className="text-muted-foreground text-sm">
            {t("subwallets.create.nameHelper")}
          </p>
        </div>
        <div className="flex gap-3">
          <Button type="button" variant="outline" onClick={() => navigate(-1)}>
            Cancel
          </Button>
          <LoadingButton loading={isLoading} type="submit">
            {t("subwallets.create.button")}
          </LoadingButton>
        </div>
      </form>
    </div>
  );
}
