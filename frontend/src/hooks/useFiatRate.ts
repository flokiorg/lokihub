import useSWR from "swr";

import { FiatRate } from "src/types";
import { swrFetcher } from "src/utils/swr";

export function useFiatRate() {
  return useSWR<FiatRate>(`/api/rates`, swrFetcher);
}
