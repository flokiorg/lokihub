import { XIcon } from "lucide-react";
import ExternalLink from "src/components/ExternalLink";

import { useLokiInfo } from "src/hooks/useLokiInfo";

export function Banner({ onDismiss }: { onDismiss: () => void }) {
  const { data: lokiInfo } = useLokiInfo();
  if (!lokiInfo) {
    return null;
  }

  // Strip 'v' prefix from version for the URL
  const versionForUrl = lokiInfo.version.replace(/^v/, "");
  const releaseUrl = `https://docs.flokicoin.org/lokihub/releases/${versionForUrl}`;

  return (
    <div className="fixed w-full bg-foreground text-background z-20 py-2 text-sm flex items-center justify-center">
      <ExternalLink
        to={releaseUrl}
        className="w-full px-12 md:px-24"
      >
        <p className="line-clamp-2 md:block whitespace-normal md:whitespace-nowrap overflow-hidden text-ellipsis text-center">
          <span className="font-semibold">Update Available</span>
          <span className="mx-2 opacity-60">→</span>
          <span className="font-mono font-medium bg-background/20 px-2 mr-2 py-0.5 rounded">{lokiInfo.version}</span>
          <span className="opacity-90">{lokiInfo.releaseNotes}</span>
        </p>
      </ExternalLink>
      <XIcon
        className="absolute right-4 cursor-pointer w-4 text-background"
        onClick={onDismiss}
      />
    </div>
  );
}
