import {
  CircleAllocation,
  CircleDailyFlow,
  CircleMemberBreakdown,
} from "src/components/circles/CircleMonitorCharts";
import { App, CircleHubStats } from "src/types";

// The circle hub's analytics panel — the content of the detail page's
// Analytics tab. A grid of per-reading cards (allocation, who holds and
// spends, daily flow) rather than one card holding bare plots. Lives in its
// own tab now, so there is no collapsible or "keep open" preference to carry
// around: opening the tab is the decision.
export function CircleAnalytics({
  hub,
  stats,
}: {
  hub: App;
  stats: CircleHubStats;
}) {
  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
      {/* Allocation is a few figures rather than a plot, so it takes the
          whole row instead of sitting half-empty beside one. The two plots
          then pair at equal height. */}
      <CircleAllocation className="lg:col-span-2" hub={hub} stats={stats} />
      <CircleMemberBreakdown stats={stats} />
      <CircleDailyFlow stats={stats} />
    </div>
  );
}
