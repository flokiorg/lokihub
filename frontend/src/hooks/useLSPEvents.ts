import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { getAuthToken } from "src/lib/auth";
import { LSPS1EventType, LSPS5EventType } from "src/types/lspsEvents";
import { useSWRConfig } from "swr";

export interface LSPS5Event {
  event: string;
  properties: {
    lsp_pubkey: string;
    method: string;
    order_id?: string;
    timestamp: string;
    timeout_block?: number;
    state?: string;
    channel_point?: string;
    error?: string;
  };
}

/**
 * Hook to listen for LSPS5 events via SSE
 * Falls back to polling if connection fails (polling logic is usually handled in the calling component)
 */
export function useLSPEvents() {
  const { mutate } = useSWRConfig();
  const [lastEvent, setLastEvent] = useState<LSPS5Event | null>(null);
  const eventSourceRef = useRef<EventSource | null>(null);

  useEffect(() => {
    // Check if EventSource is supported
    if (typeof window === "undefined" || !window.EventSource) {
      console.warn("EventSource is not supported in this browser");
      return;
    }

    // Connect to the SSE endpoint
    const token = getAuthToken();
    const url = new URL("/api/lsps5/events", window.location.origin);
    if (token) {
        url.searchParams.set("token", token);
    }

    const es = new EventSource(url.toString());
    eventSourceRef.current = es;

    es.onopen = () => {
    };

    es.onerror = (err) => {
      console.error("[useLSPEvents] EventSource error:", err);
    };

    // Listen for specific event types defined in the backend
    const eventTypes = [
      LSPS5EventType.Notification,
      LSPS5EventType.PaymentIncoming,
      LSPS5EventType.ExpirySoon,
      LSPS5EventType.LiquidityRequest,
      LSPS5EventType.OnionMessage,
      LSPS5EventType.OrderStateChanged,
      LSPS1EventType.Notification
    ];

    eventTypes.forEach(eventType => {
      es.addEventListener(eventType, (e: MessageEvent) => {
        try {
          const payload = JSON.parse(e.data) as LSPS5Event;
          setLastEvent(payload);

          // Global reactions
          switch (eventType) {
            case LSPS5EventType.PaymentIncoming:
              toast.info("LSP notified of incoming payment. Syncing wallet...");
              mutate("/api/transactions");
              mutate("/api/balances");
              break;
            case LSPS5EventType.ExpirySoon:
              toast.warning("LSP Channel expiry soon", {
                description: `Block timeout: ${payload.properties.timeout_block}`
              });
              break;
          }

          // Trigger SWR revalidation for relevant endpoints (if any)
          mutate("/api/channels");

        } catch (err) {
          console.error(`[useLSPEvents] Failed to parse ${eventType} data:`, err);
        }
      });
    });

    return () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close();
      }
    };
  }, [mutate]);

  return {
    lastEvent,
    isConnected: eventSourceRef.current?.readyState === EventSource.OPEN
  };
}
