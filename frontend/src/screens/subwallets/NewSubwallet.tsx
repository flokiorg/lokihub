import { HelpCircleIcon } from "lucide-react";
import React from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";
import AppHeader from "src/components/AppHeader";
import Loading from "src/components/Loading";
import { SubWalletInfoDialog } from "src/components/SubWalletInfoDialog";
import { Button } from "src/components/ui/button";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Input } from "src/components/ui/input";
import { Label } from "src/components/ui/label";
import { SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { useApps } from "src/hooks/useApps";
import { useInfo } from "src/hooks/useInfo";
import { createApp } from "src/requests/createApp";
import { CreateAppRequest } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";

export function NewSubwallet() {
  const navigate = useNavigate();
  const [name, setName] = React.useState("");
  const { data: appsData } = useApps(
    undefined,
    undefined,
    {
      appStoreAppId: SUBWALLET_APPSTORE_APP_ID,
    },
    "created_at"
  );
  const { data: info } = useInfo();


  const [isLoading, setLoading] = React.useState(false);

  if (!info || !appsData) {
    return <Loading />;
  }



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
        isolated: true,
        metadata: {
          app_store_app_id: SUBWALLET_APPSTORE_APP_ID,
        },
      };

      const createAppResponse = await createApp(createAppRequest);

      navigate("/sub-wallets/created", {
        state: createAppResponse,
      });

      toast("New sub-wallet created for " + name);
    } catch (error) {
      handleRequestError("Failed to create app", error);
    }
    setLoading(false);
  };

  return (
    <div className="grid gap-5">
      <AppHeader
        title="Create Sub-wallet"
        contentRight={
          <>
            <SubWalletInfoDialog
              trigger={
                <Button variant="outline">
                  <HelpCircleIcon className="w-4 h-4 mr-2" />
                  Help
                </Button>
              }
            />
          </>
        }
      />
      <form
        onSubmit={handleSubmit}
        className="flex flex-col items-start gap-3 max-w-lg"
      >
        <div className="w-full grid gap-1.5 mb-4">
          <Label htmlFor="name">Sub-wallet name</Label>
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
            Name your friend, family member or coworker
          </p>
        </div>
        <LoadingButton loading={isLoading} type="submit">
          Create Sub-wallet
        </LoadingButton>
      </form>
    </div>
  );
}
