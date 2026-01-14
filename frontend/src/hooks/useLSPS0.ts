import { useCallback, useState } from "react";
import { request } from "src/utils/request";

export interface LSPS0Protocol {
  protocols: number[];
}

/**
 * Hook for LSPS0 protocol negotiation
 * Allows querying which LSPS protocols an LSP supports
 */
export function useLSPS0(lspPubkey: string) {
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  /**
   * Lists the LSPS protocols supported by the LSP
   */
  const listProtocols = useCallback(async () => {
    if (!lspPubkey) {
      setError("LSP public key is required");
      return null;
    }

    setIsLoading(true);
    setError(null);
    
    try {
      const query = new URLSearchParams();
      query.append("lsp", lspPubkey);
      
      const response = await request<LSPS0Protocol>(`/api/lsps0/protocols?${query.toString()}`);
      return response;
    } catch (e: any) {
      const errorMsg = e.message || "Failed to fetch supported protocols";
      setError(errorMsg);
      return null;
    } finally {
      setIsLoading(false);
    }
  }, [lspPubkey]);

  return { listProtocols, isLoading, error };
}
