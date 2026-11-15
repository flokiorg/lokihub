import useSWR, { SWRConfiguration } from "swr";

import { LIST_CIRCLE_CHILDREN_LIMIT } from "src/constants";
import { ListCircleChildrenBalancesResponse } from "src/types";
import { swrFetcher } from "src/utils/swr";

const pollConfiguration: SWRConfiguration = {
  refreshInterval: 3000,
};

export function useCircleChildren(appId: number, page = 1, poll = false) {
  const offset = (page - 1) * LIST_CIRCLE_CHILDREN_LIMIT;
  const url = `/api/apps/${appId}/circle/children?limit=${LIST_CIRCLE_CHILDREN_LIMIT}&offset=${offset}`;
  return useSWR<ListCircleChildrenBalancesResponse>(
    url,
    swrFetcher,
    poll ? pollConfiguration : undefined
  );
}
