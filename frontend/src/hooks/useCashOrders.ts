import useSWR from "swr";

import type {
  OrderConnection,
  OrderSettings,
  OrdersView,
} from "src/components/cash/orders";
import { swrFetcher } from "src/utils/swr";

// The Sell-cash operator API (NIP-CO / WP-19 item 8): a Cash Hub's Order
// connections, its selling terms (null while it sells nothing) and the orders
// placed on it.
export function useOrderConnections(hubId: number | undefined) {
  return useSWR<OrderConnection[]>(
    hubId ? `/api/cash-hubs/${hubId}/order-connections` : null,
    swrFetcher
  );
}

export function useOrderTerms(hubId: number | undefined) {
  return useSWR<OrderSettings | null>(
    hubId ? `/api/cash-hubs/${hubId}/order-terms` : null,
    swrFetcher
  );
}

export function useOrders(hubId: number | undefined) {
  return useSWR<OrdersView>(
    hubId ? `/api/cash-hubs/${hubId}/orders` : null,
    swrFetcher
  );
}
