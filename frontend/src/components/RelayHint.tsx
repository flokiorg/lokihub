import { InfoIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router";

import { Alert, AlertDescription } from "src/components/ui/alert";
import { useInfo } from "src/hooks/useInfo";
import { splitRelayUrls } from "src/hooks/useNdk";

// RelayHint explains the empty state of the Nostr social features (profiles,
// notes, circles) when the user has selected no General relay under
// Settings → Services. lokihub never auto-connects to a relay the user has not
// picked, so with none selected these features have nothing to load — this is
// the expected, graceful degradation, not an error or an endless spinner.
//
// Renders nothing until /api/info has loaded (so it never flashes before the
// relay list is known) and nothing once at least one General relay is
// selected, so it is safe to drop at the top of any Nostr-backed screen.
export function RelayHint({ className }: { className?: string }) {
  const { t } = useTranslation("common");
  const { data: info } = useInfo();

  if (!info || splitRelayUrls(info.generalRelay).length > 0) {
    return null;
  }

  return (
    <Alert className={className}>
      <InfoIcon className="h-4 w-4" />
      <AlertDescription>
        <Link to="/settings/services" className="underline underline-offset-2">
          {t("relayHint.none")}
        </Link>
      </AlertDescription>
    </Alert>
  );
}

export default RelayHint;
