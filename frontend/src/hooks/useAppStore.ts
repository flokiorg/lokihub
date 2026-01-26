import { useEffect, useState } from "react";
import { AppStoreApp } from "src/components/connections/SuggestedAppData";
import { swrFetcher } from "src/utils/swr";

export const useAppStore = () => {
    const [apps, setApps] = useState<AppStoreApp[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    useEffect(() => {
        const fetchApps = async () => {
            try {
                const data = (await swrFetcher("/api/appstore/apps")) as AppStoreApp[];
                if (Array.isArray(data)) {
                    setApps(data);
                } else {
                    console.error("App Store Apps data is not an array:", data);
                    setApps([]);
                }
            } catch (e: any) {
                setError(e.message || "Failed to fetch apps");
            } finally {
                setLoading(false);
            }
        };

        fetchApps();
    }, []);

    return { apps, loading, error };
};
