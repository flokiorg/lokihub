import { FolderOpenIcon } from "lucide-react";
import React from "react";
import { toast } from "sonner";
import { useTranslation } from "react-i18next";
import Container from "src/components/Container";
import PasswordInput from "src/components/password/PasswordInput";
import TwoColumnLayoutHeader from "src/components/TwoColumnLayoutHeader";
import { Button } from "src/components/ui/button";
import { LoadingButton } from "src/components/ui/custom/loading-button";
import { Label } from "src/components/ui/label";

import { useInfo } from "src/hooks/useInfo";
import { saveAuthToken } from "src/lib/auth";
import { AuthTokenResponse } from "src/types";
import { handleRequestError } from "src/utils/handleRequestError";
import { canOpenFolder, openFolder } from "src/utils/openFolder";
import { request } from "src/utils/request";

// After this long on the "Starting…" screen with the backend reachable but
// neither started nor failed (e.g. the node connection is hanging), surface a
// hint and an escape hatch instead of spinning forever with no feedback.
const STARTUP_SLOW_MS = 120_000;

export default function Start() {
  const [unlockPassword, setUnlockPassword] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  const [tookTooLong, setTookTooLong] = React.useState(false);
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

  // The backend can stay reachable but report neither running nor a startup
  // error (e.g. the node connection hangs), which would otherwise leave this
  // screen polling forever with no feedback. Surface a hint after a while.
  React.useEffect(() => {
    if (!loading) {
      setTookTooLong(false);
      return;
    }
    const timer = setTimeout(() => setTookTooLong(true), STARTUP_SLOW_MS);
    return () => clearTimeout(timer);
  }, [loading]);

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
              {loading && tookTooLong && (
                <div className="flex flex-col items-center gap-2 text-center">
                  <p className="text-muted-foreground text-xs">
                    {t("start.takingLong")}
                  </p>
                  <div className="flex gap-2">
                    {canOpenFolder(info?.workDir) && (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => openFolder(info!.workDir)}
                      >
                        <FolderOpenIcon className="w-4 h-4 me-2" />
                        {t("start.openDataFolder")}
                      </Button>
                    )}
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={() => window.location.reload()}
                    >
                      {t("start.reload")}
                    </Button>
                  </div>
                </div>
              )}
            </div>
          </form>
        </div>
      </Container>
    </>
  );
}
