import { useCallback, useState } from "react";
import { LSPS2BuyResponse, LSPS2GetInfoResponse } from "src/types";
import { request } from "src/utils/request";

export function useLSPS2(lspPubkey: string) {
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const getInfo = useCallback(async () => {
    setIsLoading(true);
    setError(null);
    try {
      const query = new URLSearchParams();
      query.append("lsp", lspPubkey);
      
      const response = await request<LSPS2GetInfoResponse>(`/api/lsps2/info?${query.toString()}`);
      return response;
    } catch (e: any) {
      setError(e.message || "Failed to fetch LSPS2 info");
      return null;
    } finally {
      setIsLoading(false);
    }
  }, [lspPubkey]);

  const buy = useCallback(async (paymentSizeMloki: number) => {
    setIsLoading(true);
    setError(null);
    try {
      const response = await request<LSPS2BuyResponse>("/api/lsps2/buy", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          lspPubkey: lspPubkey,
          paymentSizeMloki: paymentSizeMloki
        })
      });
      return response;
    } catch (e: any) {
      setError(e.message || "Failed to buy JIT liquidity");
      return null;
    } finally {
      setIsLoading(false);
    }
  }, [lspPubkey]);

  return { getInfo, buy, isLoading, error };
}
