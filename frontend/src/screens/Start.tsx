import React from "react";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";
import Container from "src/components/Container";
import PasswordInput from "src/components/password/PasswordInput";
import TwoColumnLayoutHeader from "src/components/TwoColumnLayoutHeader";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Label } from "src/components/ui/label";

import { useInfo } from "src/hooks/useInfo";
import { saveAuthToken } from "src/lib/auth";
import { AuthTokenResponse } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { request } from "src/utils/request";

export default function Start() {
  const [unlockPassword, setUnlockPassword] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  const { t } = useTranslation("setup");
  const [buttonText, setButtonText] = React.useState("");

  const { data: info } = useInfo(true); // poll the info endpoint to auto-redirect when app is running

  const startupState = info?.startupState;
  const startupError = info?.startupError;
  const startupErrorTime = info?.startupErrorTime;

  React.useEffect(() => {
    if (startupState) {
      setButtonText(startupState);
    }
  }, [startupState]);

  React.useEffect(() => {
    if (startupError && startupErrorTime) {
      toast.error(t("start.failedToStart"), {
        description: startupError,
      });
      setLoading(false);
      setButtonText(t("start.title"));
      setUnlockPassword("");
    }
  }, [startupError, startupErrorTime]);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    try {
      setLoading(true);
      setButtonText(t("start.pleaseWait"));

      const authTokenResponse = await request<AuthTokenResponse>("/api/start", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          unlockPassword,
          permission: "full",
        }),
      });
      if (authTokenResponse) {
        saveAuthToken(authTokenResponse.token);
      }
    } catch (error) {
      handleRequestError(t("start.failedToConnect"), error);
      setLoading(false);
      setButtonText(t("start.title"));
      setUnlockPassword("");
    }
  }

  return (
    <>
      <Container>
        <div className="mx-auto grid gap-5">
          <TwoColumnLayoutHeader
            title={t("start.title")}
            description={t("start.description")}
          />
          <form onSubmit={onSubmit}>
            <div className="grid gap-4">
              <div className="grid gap-1.5">
                <Label htmlFor="password">{t("start.passwordLabel")}</Label>
                <PasswordInput
                  id="password"
                  onChange={setUnlockPassword}
                  autoFocus
                  value={unlockPassword}
                />
              </div>
              <LoadingButton
                type="submit"
                loading={loading}
                disabled={!unlockPassword}
              >
                {buttonText || t("start.title")}
              </LoadingButton>
              {loading && (
                <p className="text-muted-foreground text-xs text-center">
                  {t("start.starting")}
                </p>
              )}
            </div>
          </form>
        </div>
      </Container>
    </>
  );
}
