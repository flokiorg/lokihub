import { OnchainTransaction } from "src/types";
import { swrFetcher } from "src/utils/swr";
import { SWRConfiguration } from "swr";
import useSWRInfinite from "swr/infinite";

const ONCHAIN_TRANSACTIONS_LIMIT = 100;

const pollConfiguration: SWRConfiguration = {
  refreshInterval: 30000,
};

export function useOnchainTransactions() {
  const getKey = (
    pageIndex: number,
    previousPageData: OnchainTransaction[]
  ) => {
    if (previousPageData && !previousPageData.length) return null; // reached the end
    return `/api/node/transactions?limit=${ONCHAIN_TRANSACTIONS_LIMIT}&offset=${pageIndex * ONCHAIN_TRANSACTIONS_LIMIT}`;
  };

  const { data, size, setSize, isLoading, isValidating, error } = useSWRInfinite<
    OnchainTransaction[]
  >(getKey, swrFetcher, {
    ...pollConfiguration,
    parallel: true,
  });

  const transactions = data ? data.flat() : [];
  const isEmpty = data?.[0]?.length === 0;
  const isReachingEnd =
    isEmpty || (data && data[data.length - 1]?.length < ONCHAIN_TRANSACTIONS_LIMIT);
  const isLoadingMore = isLoading || (size > 0 && data && typeof data[size - 1] === "undefined");

  return {
    transactions,
    size,
    setSize,
    isLoading,
    isValidating,
    isReachingEnd,
    isLoadingMore,
    error,
  };
}
