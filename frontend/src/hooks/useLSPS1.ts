import { useCallback, useState } from "react";
import { LSPS1CreateOrderRequest, LSPS1CreateOrderResponse, LSPS1GetInfoResponse, LSPS1GetOrderResponse, LSPS1ListOrdersResponse } from "src/types";
import { request } from "src/utils/request";

export function useLSPS1(lspPubkey: string) {
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const getInfo = useCallback(async (token?: string) => {
    setIsLoading(true);
    setError(null);
    try {
      const query = new URLSearchParams();
      query.append("lsp", lspPubkey);
      if (token) query.append("token", token);
      
      const response = await request<LSPS1GetInfoResponse>(`/api/lsps1/info?${query.toString()}`);
      return response;
    } catch (e: any) {
      setError(e.message || "Failed to fetch LSP info");
      return null;
    } finally {
      setIsLoading(false);
    }
  }, [lspPubkey]);

  const createOrder = useCallback(async (params: LSPS1CreateOrderRequest) => {
      setIsLoading(true);
      setError(null);
      try {
          const response = await request<LSPS1CreateOrderResponse>("/api/lsps1/order", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify(params)
          });
          return response;
      } catch (e: any) {
          setError(e.message || "Failed to create order");
          return null;
      } finally {
          setIsLoading(false);
      }
  }, []);

  const getOrder = useCallback(async (orderId: string) => {
      setIsLoading(true);
      setError(null);
      try {
          const response = await request<LSPS1GetOrderResponse>(`/api/lsps1/order?orderId=${orderId}&lsp=${lspPubkey}`);
          return response;
      } catch (e: any) {
          setError(e.message || "Failed to fetch order");
          return null;
      } finally {
          setIsLoading(false);
      }
  }, [lspPubkey]);

  const listOrders = useCallback(async () => {
    setIsLoading(true);
    setError(null);
    try {
        const response = await request<LSPS1ListOrdersResponse>("/api/lsps1/orders");
        return response?.orders || [];
    } catch (e: any) {
        setError(e.message || "Failed to list orders");
        return [];
    } finally {
        setIsLoading(false);
    }
  }, []);

  return { getInfo, createOrder, getOrder, listOrders, isLoading, error };
}

