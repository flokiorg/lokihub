import React from "react";
import { useTranslation } from "react-i18next";
import {
  CircleAllocation,
  CircleDailyFlow,
  CircleMemberBreakdown,
} from "src/components/circles/CircleMonitorCharts";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "src/components/ui/accordion";
import { Checkbox } from "src/components/ui/checkbox";
import { localStorageKeys } from "src/constants";
import { App, CircleHubStats } from "src/types";

// The circle hub's analytics section, matching the cash hubs' exactly: a
// collapsible heading with a remembered "keep open", and a grid of cards
// rather than one card holding four bare plots.
//
// Its own storage key: wanting a circle's charts open is a different decision
// from wanting a cash hub's, and a host who works mostly in one should not
// have the other follow it around.
export function CircleAnalytics({
  hub,
  stats,
}: {
  hub: App;
  stats: CircleHubStats;
}) {
  const { t } = useTranslation("apps");

  const [keepOpen, setKeepOpen] = React.useState(
    () =>
      localStorage.getItem(localStorageKeys.circleAnalyticsKeepOpen) === "true"
  );
  const [isOpen, setOpen] = React.useState(keepOpen);

  const toggleKeepOpen = (checked: boolean) => {
    setKeepOpen(checked);
    localStorage.setItem(
      localStorageKeys.circleAnalyticsKeepOpen,
      String(checked)
    );
  };

  return (
    <Accordion
      type="single"
      collapsible
      value={isOpen ? "analytics" : ""}
      onValueChange={(v) => setOpen(v === "analytics")}
    >
      <AccordionItem value="analytics" className="border-b-0">
        {/* Checkbox as a sibling of the trigger, not a child: the trigger is
            a <button>, so a control nested inside it could not be clicked
            without also toggling the section. */}
        <div className="flex items-center gap-3 px-1">
          <AccordionTrigger className="flex-1 py-0 text-base font-semibold">
            {t("circleHub.analyticsTitle")}
          </AccordionTrigger>
          {isOpen && (
            <label className="text-muted-foreground flex shrink-0 cursor-pointer items-center gap-2 text-sm font-normal">
              <Checkbox
                checked={keepOpen}
                onCheckedChange={(checked) => toggleKeepOpen(checked === true)}
              />
              {t("circleHub.keepAnalyticsOpen")}
            </label>
          )}
        </div>
        <AccordionContent className="pt-4 pb-0">
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            {/* Allocation is a few figures rather than a plot, so it takes
                the whole row instead of sitting half-empty beside one. The
                two plots then pair at equal height. */}
            <CircleAllocation
              className="lg:col-span-2"
              hub={hub}
              stats={stats}
            />
            <CircleMemberBreakdown stats={stats} />
            <CircleDailyFlow stats={stats} />
          </div>
        </AccordionContent>
      </AccordionItem>
    </Accordion>
  );
}
