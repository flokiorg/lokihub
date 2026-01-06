import useSWR from "swr";

import { FlokicoinRate } from "src/types";
import { swrFetcher } from "src/utils/swr";

export function useFlokicoinRate() {
  return useSWR<FlokicoinRate>(`/api/loki/rates`, swrFetcher);
}
