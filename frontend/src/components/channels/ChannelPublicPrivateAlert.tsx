import { AlertCircleIcon } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "src/components/ui/alert";

export function ChannelPublicPrivateAlert() {
  return (
    <Alert>
      <AlertCircleIcon />
      <AlertTitle>Conflicting Private / Public Channels</AlertTitle>
      <AlertDescription>
        <div className="mb-2">
          You will not be able to receive payments on any private channels. It
          is recommended to only open all private or all public channels.
        </div>
      </AlertDescription>
    </Alert>
  );
}
