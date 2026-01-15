import { useCallback, useState } from "react";
import { request } from "src/utils/request";

export interface LSPS2FeeParams {
  minFeeMloki: number;
  proportional: number;
  validUntil: string;
  minLifetime: number;
  maxClientToSelfDelay: number;
  minPaymentSizeMloki: number;
  maxPaymentSizeMloki: number;
  promise: string;
}

export interface LSPS2GetInfoResponse {
  openingFeeParamsMenu: LSPS2FeeParams[];
}

export interface LSPS2BuyRequest {
  lspPubkey: string;
  paymentSizeMloki: number;
  openingFeeParams: LSPS2FeeParams;
}

export interface LSPS2BuyResponse {
  jitChannelSCID: string;
  lspCltvExpiryDelta: number;
  clientTrusts0Conf: boolean;
  lspNodeID: string;
}

/**
 * Hook for LSPS2 JIT (Just-In-Time) channel operations
 * Enables purchasing inbound liquidity on-demand when receiving payments
 */
export function useLSPS2(lspPubkey: string) {
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  /**
   * Fetches available JIT channel fee parameters from the LSP
   */
  const getInfo = useCallback(async (token?: string) => {
    if (!lspPubkey) {
      setError("LSP public key is required");
      return null;
    }

    setIsLoading(true);
    setError(null);
    
    try {
      const query = new URLSearchParams();
      query.append("lsp", lspPubkey);
      if (token) query.append("token", token);
      
      const response = await request<LSPS2GetInfoResponse>(`/api/lsps2/info?${query.toString()}`);
      return response;
    } catch (e: any) {
      const errorMsg = e.message || "Failed to fetch JIT channel info";
      setError(errorMsg);
      return null;
    } finally {
      setIsLoading(false);
    }
  }, [lspPubkey]);

  /**
   * Requests a JIT channel from the LSP
   * Returns the channel SCID and CLTV delta to be embedded in the invoice
   */
  const buy = useCallback(async (params: LSPS2BuyRequest) => {
    setIsLoading(true);
    setError(null);
    
    try {
      const response = await request<LSPS2BuyResponse>("/api/lsps2/buy", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(params)
      });
      return response;
    } catch (e: any) {
      const errorMsg = e.message || "Failed to purchase JIT channel";
      setError(errorMsg);
      return null;
    } finally {
      setIsLoading(false);
    }
  }, []);

  return { getInfo, buy, isLoading, error };
}
