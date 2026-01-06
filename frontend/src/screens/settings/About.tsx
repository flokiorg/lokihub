import Loading from "src/components/Loading";
import SettingsHeader from "src/components/SettingsHeader";
import { Badge } from "src/components/ui/badge";

import { useInfo } from "src/hooks/useInfo";

export function About() {
  const { data: info } = useInfo();
  if (!info) {
    return <Loading />;
  }


  return (
    <>
      <SettingsHeader title="About" description="Info about your Lokihub" />
      <div className="grid gap-4">
        <div className="grid gap-2">
          <p className="font-medium text-sm">Lokihub Version</p>
          <p className="text-muted-foreground text-sm slashed-zero">
            {info.version || "dev"}
          </p>
        </div>
        <div className="grid gap-2">
          <p className="font-medium text-sm">Lightning Node Backend</p>
          <p className="text-muted-foreground text-sm slashed-zero">
            {info.backendType}
          </p>
        </div>
        {info.unlocked && info.workDir && (
          <div className="grid gap-2">
            <p className="font-medium text-sm">Work Directory</p>
            <p className="text-muted-foreground text-sm slashed-zero break-all">
              {info.workDir}
            </p>
          </div>
        )}
        <div className="grid gap-2">
          <p className="font-medium text-sm">Nostr Relays</p>
          {info.relays.map((relay) => (
            <p className="flex items-center gap-2 text-muted-foreground text-sm">
              {relay.url}
              <Badge variant={relay.online ? "positive" : "destructive"}>
                {relay.online ? "online" : "offline"}
              </Badge>
            </p>
          ))}
        </div>
      </div>
    </>
  );
}
