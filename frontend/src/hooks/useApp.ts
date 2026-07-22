import useSWR from "swr";

import { App } from "src/types";
import { swrFetcher } from "src/utils/swr";

// Circle Hub cards show a "following count" enriched from a non-blocking
// cache peek (see CircleIdentitySummaryWithCounts) that may still be
// unpopulated on the first response — poll until it resolves instead of
// leaving the card's "Syncing…" state stuck forever, regardless of the
// caller's own `poll` setting.
function needsCircleFollowingSync(app: App | undefined) {
  return (
    app?.circleIdentity?.policy === "following" &&
    app.circleIdentity.followingCount === undefined
  );
}

export function useApp(id: number | undefined, poll = false) {
  return useSWR<App>(!!id && `/api/apps/${id}`, swrFetcher, {
    refreshInterval: (data) =>
      poll || needsCircleFollowingSync(data) ? 3000 : 0,
  });
}
