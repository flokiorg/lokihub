import React from "react";
import useSWR from "swr";

import { App } from "src/types";
import { swrFetcher } from "src/utils/swr";

// A follow-set circle's member count comes from a cache that may not have
// read the set yet, so the first response can lack it. Poll briefly until it
// arrives instead of leaving the card's "Reading…" state stuck. The cap stops
// a set that isn't on any relay from being re-queried forever.
const MAX_FOLLOW_SET_POLLS = 20;

function needsFollowSetRead(app: App | undefined) {
  return (
    app?.circleIdentity?.policy === "follow_set" &&
    app.circleIdentity.memberCount === undefined
  );
}

export function useApp(id: number | undefined, poll = false) {
  const followSetPolls = React.useRef(0);
  return useSWR<App>(!!id && `/api/apps/${id}`, swrFetcher, {
    refreshInterval: (data) => {
      if (poll) {
        return 3000;
      }
      if (
        needsFollowSetRead(data) &&
        followSetPolls.current < MAX_FOLLOW_SET_POLLS
      ) {
        followSetPolls.current++;
        return 3000;
      }
      return 0;
    },
  });
}
