import { useCallback, useState } from "react";
import { request } from "src/utils/request";

export interface LSPS5Webhook {
  url: string;
  secret: string;
}

export interface LSPS5SetWebhookRequest {
  lspPubkey: string;
  url: string;
  secret: string;
  appName?: string;
}

export interface LSPS5ListWebhooksResponse {
  webhooks: Array<{
    url: string;
    appName?: string;
  }>;
}

export interface LSPS5RemoveWebhookRequest {
  lspPubkey: string;
  url: string;
}

/**
 * Hook for LSPS5 webhook management
 * Allows registering webhooks for receiving notifications about incoming payments
 */
export function useLSPS5(lspPubkey: string) {
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  /**
   * Registers a webhook URL with the LSP
   * The LSP will send notifications to this URL for events like incoming payments
   */
  const setWebhook = useCallback(async (params: LSPS5SetWebhookRequest) => {
    setIsLoading(true);
    setError(null);
    
    try {
      await request("/api/lsps5/webhook", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(params)
      });
      return true;
    } catch (e: any) {
      const errorMsg = e.message || "Failed to register webhook";
      setError(errorMsg);
      return false;
    } finally {
      setIsLoading(false);
    }
  }, []);

  /**
   * Lists all registered webhooks for the connected LSP
   */
  const listWebhooks = useCallback(async () => {
    if (!lspPubkey) {
      setError("LSP public key is required");
      return null;
    }

    setIsLoading(true);
    setError(null);
    
    try {
      const query = new URLSearchParams();
      query.append("lsp", lspPubkey);
      
      const response = await request<LSPS5ListWebhooksResponse>(`/api/lsps5/webhooks?${query.toString()}`);
      return response;
    } catch (e: any) {
      const errorMsg = e.message || "Failed to list webhooks";
      setError(errorMsg);
      return null;
    } finally {
      setIsLoading(false);
    }
  }, [lspPubkey]);

  /**
   * Removes a webhook registration from the LSP
   */
  const removeWebhook = useCallback(async (params: LSPS5RemoveWebhookRequest) => {
    setIsLoading(true);
    setError(null);
    
    try {
      const query = new URLSearchParams();
      query.append("lsp", params.lspPubkey);
      query.append("url", params.url);
      
      await request(`/api/lsps5/webhook?${query.toString()}`, {
        method: "DELETE"
      });
      return true;
    } catch (e: any) {
      const errorMsg = e.message || "Failed to remove webhook";
      setError(errorMsg);
      return false;
    } finally {
      setIsLoading(false);
    }
  }, []);

  return { setWebhook, listWebhooks, removeWebhook, isLoading, error };
}
