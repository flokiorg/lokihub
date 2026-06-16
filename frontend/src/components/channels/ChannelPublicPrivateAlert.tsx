import { AlertCircleIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";

export function ChannelPublicPrivateAlert() {
  const { t } = useTranslation("channels");

  return (
    <Alert>
      <AlertCircleIcon />
      <AlertTitle>{t("alerts.conflictingChannels.title")}</AlertTitle>
      <AlertDescription>
        <div className="mb-2">{t("alerts.conflictingChannels.desc")}</div>
      </AlertDescription>
    </Alert>
  );
}
