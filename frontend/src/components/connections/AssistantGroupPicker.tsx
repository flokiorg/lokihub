import { TriangleAlertIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "src/components/ui/badge";
import { Tabs, TabsList, TabsTrigger } from "src/components/ui/tabs";
import {
  AI_GROUPS,
  AssistantLevel,
  AssistantLevels,
} from "src/hooks/useAIAssistants";

// The six capability groups as Off / Read / Read & change, with the read scope
// spelled out and the write risk shown in warning colour whenever Read & change
// is chosen — destructive power is never hidden behind a toggle.
export default function AssistantGroupPicker({
  levels,
  onChange,
}: {
  levels: AssistantLevels;
  onChange: (levels: AssistantLevels) => void;
}) {
  const { t } = useTranslation("apps");
  return (
    <div className="flex flex-col divide-y divide-border">
      {AI_GROUPS.map((g) => (
        <div key={g.id} className="flex flex-col gap-2 py-4 first:pt-0">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <span className="font-medium">{g.title}</span>
              {g.advanced && (
                <Badge variant="outline">
                  {t("assistant.advanced", "Advanced")}
                </Badge>
              )}
            </div>
            <Tabs
              value={levels[g.id]}
              onValueChange={(v) =>
                onChange({ ...levels, [g.id]: v as AssistantLevel })
              }
            >
              <TabsList>
                <TabsTrigger value="off">
                  {t("assistant.off", "Off")}
                </TabsTrigger>
                <TabsTrigger value="read">
                  {t("assistant.read", "Read")}
                </TabsTrigger>
                <TabsTrigger value="write">
                  {t("assistant.write", "Read & change")}
                </TabsTrigger>
              </TabsList>
            </Tabs>
          </div>
          <p className="text-sm text-muted-foreground">
            {levels[g.id] === "off"
              ? t("assistant.noAccess", "No access.")
              : g.read}
          </p>
          {levels[g.id] === "write" && (
            <p className="flex gap-2 text-sm text-warning-foreground">
              <TriangleAlertIcon className="w-4 h-4 shrink-0 mt-0.5" />
              {g.writeRisk}
            </p>
          )}
        </div>
      ))}
    </div>
  );
}
