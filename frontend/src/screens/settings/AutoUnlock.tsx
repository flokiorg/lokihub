import { AlertTriangleIcon } from "lucide-react";
import React from "react";
import { useTranslation } from "react-i18next";

import { toast } from "sonner";
import Loading from "src/components/Loading";
import PasswordInput from "src/components/password/PasswordInput";
import SettingsHeader from "src/components/SettingsHeader";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Label } from "src/components/ui/label";

import { useInfo } from "src/hooks/useInfo";
import { request } from "src/utils/request";

export function AutoUnlock() {
  const { t } = useTranslation("settings");
  const { data: info, mutate: refetchInfo } = useInfo();

  const [unlockPassword, setUnlockPassword] = React.useState("");
  const [loading, setLoading] = React.useState(false);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    try {
      setLoading(true);
      await request("/api/auto-unlock", {
        method: "PATCH",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          unlockPassword,
        }),
      });
      await refetchInfo();
      setUnlockPassword("");
      toast(
        `Successfully ${unlockPassword ? "enabled" : "disabled"} auto-unlock`
      );
    } catch (error) {
      toast("Auto Unlock change failed", {
        description: (error as Error).message,
      });
    } finally {
      setLoading(false);
    }
  };

  if (!info) {
    return <Loading />;
  }
  if (!info.autoUnlockPasswordSupported) {
    return <p>Your Hub does not support this feature.</p>;
  }

  return (
    <>
      <SettingsHeader
        title={t("autoUnlock.title")}
        description={t("autoUnlock.description")}
      />
      <div>
        <p className="text-muted-foreground">{t("autoUnlock.bodyText")}</p>
        <Alert className="mt-3">
          <AlertTriangleIcon />
          <AlertTitle>{t("autoUnlock.attention")}</AlertTitle>
          <AlertDescription>{t("autoUnlock.attentionDesc")}</AlertDescription>
        </Alert>
        {!info.autoUnlockPasswordEnabled && (
          <>
            <form
              onSubmit={onSubmit}
              className="w-full md:w-96 flex flex-col gap-4 mt-4"
            >
              <div className="grid gap-2">
                <Label htmlFor="unlock-password">
                  {t("autoUnlock.passwordLabel")}
                </Label>
                <PasswordInput
                  id="unlock-password"
                  autoFocus
                  onChange={setUnlockPassword}
                  value={unlockPassword}
                />
              </div>
              <div>
                <LoadingButton loading={loading}>
                  {t("autoUnlock.enableButton")}
                </LoadingButton>
              </div>
            </form>
          </>
        )}
        {info.autoUnlockPasswordEnabled && (
          <>
            <form
              onSubmit={onSubmit}
              className="w-full md:w-96 flex flex-col gap-4 mt-4"
            >
              <div>
                <LoadingButton loading={loading}>
                  {t("autoUnlock.disableButton")}
                </LoadingButton>
              </div>
            </form>
          </>
        )}
      </div>
    </>
  );
}
