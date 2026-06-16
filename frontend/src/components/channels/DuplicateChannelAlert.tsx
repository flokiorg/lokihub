import { TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";
import { useChannels } from "src/hooks/useChannels";

type PeerAlertProps = {
  pubkey?: string;
  name?: string;
};

export function DuplicateChannelAlert({ pubkey, name }: PeerAlertProps) {
  const { t } = useTranslation("channels");
  const { data: channels } = useChannels();

  if (!pubkey) {
    return null;
  }

  const matchedPeer = channels?.find((p) => p.remotePubkey === pubkey);

  if (!matchedPeer) {
    return null;
  }

  const hasNamedPeer = name && name !== "Custom";

  return (
    <Alert>
      <TriangleAlertIcon />
      <AlertTitle>
        {hasNamedPeer
          ? t("alerts.duplicateChannel.titleWithName", { name })
          : t("alerts.duplicateChannel.titleGeneric")}
      </AlertTitle>
      <AlertDescription>
        {t("alerts.duplicateChannel.desc")}
      </AlertDescription>
    </Alert>
  );
}
