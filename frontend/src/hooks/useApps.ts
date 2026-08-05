import useSWR from "swr";

import React from "react";
import { AppStoreApp } from "src/components/connections/SuggestedAppData";
import { LIST_APPS_LIMIT, SUBWALLET_APPSTORE_APP_ID } from "src/constants";
import { ListAppsResponse } from "src/types";
import { swrFetcher } from "src/utils/swr";

export function useApps(
  limit = LIST_APPS_LIMIT,
  page = 1,
  filters?: {
    name?: string;
    appStoreAppId?: string;
    unused?: boolean;
    subWallets?: boolean;
    kind?: string;
    topLevelOnly?: boolean;
    parentAppId?: number;
  },
  orderBy?: "last_used_at" | "created_at",
  isEnabled = true
) {
  const offset = (page - 1) * limit;
  return useSWR<ListAppsResponse>(
    isEnabled
      ? `/api/apps?limit=${limit}&offset=${offset}&filters=${JSON.stringify(filters || {})}&order_by=${orderBy || ""}`
      : undefined,
    swrFetcher,
    {
      // Circle Hub cards show a "following count" enriched from a
      // non-blocking cache peek (see CircleIdentitySummaryWithCounts) that
      // may still be unpopulated on the first response — poll until it
      // resolves instead of leaving the card's skeleton stuck forever.
      refreshInterval: (data) =>
        data?.apps.some(
          (app) =>
            app.circleIdentity?.policy === "following" &&
            app.circleIdentity.followingCount === undefined
        )
          ? 3000
          : 0,
    }
  );
}

export function useAppsForAppStoreApp(appStoreApp: AppStoreApp | undefined) {
  const isStoreApp = !!appStoreApp?.id;

  const { data: connectedAppsByAppStoreId } = useApps(
    undefined,
    undefined,
    {
      appStoreAppId: appStoreApp?.id,
    },
    undefined,
    isStoreApp
  );

  const connectedApps = React.useMemo(
    () => {
      if (!isStoreApp) {
        return undefined;
      }

      return connectedAppsByAppStoreId?.apps
        ? [...connectedAppsByAppStoreId.apps].filter(
            (v, i, a) => a.findIndex((value) => value.id === v.id) === i
          )
        : undefined;
    },
    [connectedAppsByAppStoreId, isStoreApp]
  );
  return connectedApps;
}

// useSiblingHubs groups top-level subwallets of the same kind together —
// e.g. viewing one Cash Hub, list the user's other Cash Hubs. Mirrors
// useAppsForAppStoreApp's shape but for kinds with no useful app_store_app_id
// of their own (every subwallet shares the same "lokies" sentinel there, see
// constants.SUBWALLET_APPSTORE_APP_ID). kind should be one of
// cash_hub/circle_hub/isolated — never a *_wallet child kind.
// The appStoreAppId filter here is load-bearing, not incidental: cash_hub and
// isolated are also assignable to a regular external app connection (e.g. via
// the generic New Connection flow's "Cash Hub"/"isolated" toggles — see
// NewApp.tsx), so "same kind" alone would wrongly group an unrelated real app
// (e.g. a "Zapf" connection granted cash_hub scope) in with the user's actual
// managed Cash Hubs. Only apps minted by the dedicated
// NewCashHub/NewCircleHub/NewSimpleSubwallet wizards carry this sentinel.
export function useSiblingHubs(kind: string | undefined) {
  const isEnabled = !!kind;

  const { data } = useApps(
    undefined,
    undefined,
    { kind, topLevelOnly: true, appStoreAppId: SUBWALLET_APPSTORE_APP_ID },
    undefined,
    isEnabled
  );

  return React.useMemo(
    () => (isEnabled ? data?.apps : undefined),
    [data, isEnabled]
  );
}

// useHubChildren groups one specific hub's children together — e.g. viewing
// one cash_wallet, list the other wallets under the same Cash Hub.
// parentAppId should come from App.parentAppId (undefined for a hub itself
// or a standalone app, which have no parent).
export function useHubChildren(parentAppId: number | undefined) {
  const isEnabled = !!parentAppId;

  const { data } = useApps(
    undefined,
    undefined,
    { parentAppId },
    undefined,
    isEnabled
  );

  return React.useMemo(
    () => (isEnabled ? data?.apps : undefined),
    [data, isEnabled]
  );
}
