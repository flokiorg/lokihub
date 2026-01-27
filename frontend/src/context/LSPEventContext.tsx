import { createContext, ReactNode, useContext, useEffect, useRef, useState } from "react";
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

interface LSPEventContextType {
  lastEvent: LSPS5Event | null;
  isConnected: boolean;
}

const LSPEventContext = createContext<LSPEventContextType | undefined>(undefined);

export function LSPEventProvider({ children }: { children: ReactNode }) {
  const { mutate } = useSWRConfig();
  const [lastEvent, setLastEvent] = useState<LSPS5Event | null>(null);
  const [isConnected, setIsConnected] = useState(false);
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
      setIsConnected(true);
    };

    es.onerror = (err) => {
      console.error("[LSPEventProvider] EventSource error:", err);
      setIsConnected(false);
      // Optional: Add reconnection logic or let browser handle partial retries
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

          // Trigger SWR revalidation for relevant endpoints
          mutate("/api/channels");

        } catch (err) {
          console.error(`[LSPEventProvider] Failed to parse ${eventType} data:`, err);
        }
      });
    });

    return () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close();
        setIsConnected(false);
      }
    };
  }, [mutate]);

  return (
    <LSPEventContext.Provider value={{ lastEvent, isConnected }}>
      {children}
    </LSPEventContext.Provider>
  );
}

export function useLSPEventContext() {
  const context = useContext(LSPEventContext);
  if (context === undefined) {
    throw new Error("useLSPEventContext must be used within a LSPEventProvider");
  }
  return context;
}
