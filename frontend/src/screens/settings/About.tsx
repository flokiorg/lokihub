import { useTranslation } from "react-i18next";
import Loading from "src/components/Loading";
import SettingsHeader from "src/components/SettingsHeader";
import { Badge } from "src/components/ui/badge";

import { useInfo } from "src/hooks/useInfo";

export function About() {
  const { t } = useTranslation("settings");
  const { data: info } = useInfo();
  if (!info) {
    return <Loading />;
  }

  return (
    <>
      <SettingsHeader
        title={t("about.title")}
        description={t("about.description")}
      />
      <div className="grid gap-4">
        <div className="grid gap-2">
          <p className="font-medium text-sm">{t("about.version")}</p>
          <p className="text-muted-foreground text-sm slashed-zero">
            {info.version || "dev"}
          </p>
        </div>
        <div className="grid gap-2">
          <p className="font-medium text-sm">{t("about.nodeBackend")}</p>
          <p className="text-muted-foreground text-sm slashed-zero">
            {info.backendType}
          </p>
        </div>
        {info.unlocked && info.workDir && (
          <div className="grid gap-2">
            <p className="font-medium text-sm">{t("about.workDir")}</p>
            <p className="text-muted-foreground text-sm slashed-zero break-all">
              {info.workDir}
            </p>
          </div>
        )}
        <div className="grid gap-2">
          <p className="font-medium text-sm">{t("about.nostrRelays")}</p>
          {info.relays.map((relay) => (
            <p className="flex items-center gap-2 text-muted-foreground text-sm">
              {relay.url}
              <Badge variant={relay.online ? "positive" : "destructive"}>
                {relay.online ? t("about.online") : t("about.offline")}
              </Badge>
            </p>
          ))}
        </div>
      </div>
    </>
  );
}
