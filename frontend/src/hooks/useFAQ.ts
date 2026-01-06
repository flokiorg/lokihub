import useSWR from "swr";

import { FAQ } from "src/types";
import { swrFetcher } from "src/utils/swr";

export function useFAQ() {
  const { data, error, isLoading } = useSWR<FAQ[]>("/api/loki/faq", swrFetcher);

  return {
    faq: data,
    error,
    isLoading,
  };
}
