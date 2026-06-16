import useSWR from "swr";
import { AppStoreApp } from "src/components/connections/SuggestedAppData";
import { swrFetcher } from "src/utils/swr";

export const useAppStore = () => {
    const { data, isLoading, error } = useSWR<AppStoreApp[]>(
        "/api/appstore/apps",
        swrFetcher,
        { dedupingInterval: 60_000 }
    );

    return {
        apps: data ?? [],
        loading: isLoading,
        error: error?.message ?? null,
    };
};
