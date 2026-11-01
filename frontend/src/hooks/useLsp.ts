import useSWR, { SWRConfiguration } from "swr";

import {
  LspAnalytics,
  LspAnnounceState,
  LspConfigResponse,
  LspJitChannel,
  LspNodesResponse,
  LspOrder,
  LspOrderDetail,
  LspPeer,
  LspSummary,
} from "src/screens/lsp/types";
import { LSPServiceInfo } from "src/types";
import { request } from "src/utils/request";
import { swrFetcher } from "src/utils/swr";

// Live events for the backoffice are v1 = SWR polling (plan §2.4). A Hub-side
// SSE bridge that forwards lspd's /dashboard/ws is deferred: it is a backend
// change (a NIP-98 auth-before-upgrade WS client + event re-emission) that is
// not low-risk, so it is intentionally out of scope here. Intervals below are
// chosen per screen so an attached operator sees fresh data without hammering
// lspd through the Hub proxy.
const poll = (ms: number): SWRConfiguration => ({ refreshInterval: ms });

// useLspStatus reads the attach status detail (configured/reachable/attached/
// endpoint/pubkey/version/error). /api/lsp/status never errors — it returns
// {configured:false} when no lspd is configured.
export function useLspStatus() {
  return useSWR<LSPServiceInfo>("/api/lsp/status", swrFetcher, poll(15000));
}

export function useLspSummary() {
  return useSWR<LspSummary>("/api/lsp/summary", swrFetcher, poll(10000));
}

export function useLspOrders() {
  // lspd returns null for an empty list; normalize to [].
  return useSWR<LspOrder[]>(
    "/api/lsp/orders",
    async (url: string) => (await swrFetcher(url)) ?? [],
    poll(15000)
  );
}

export function useLspOrderDetail(orderId: string | undefined) {
  return useSWR<LspOrderDetail>(
    orderId ? `/api/lsp/orders/${encodeURIComponent(orderId)}` : null,
    swrFetcher,
    poll(15000)
  );
}

export function useLspPeers() {
  return useSWR<LspPeer[]>(
    "/api/lsp/peers",
    async (url: string) => (await swrFetcher(url)) ?? [],
    poll(20000)
  );
}

export function useLspJitChannels() {
  return useSWR<LspJitChannel[]>(
    "/api/lsp/jit-channels",
    async (url: string) => (await swrFetcher(url)) ?? [],
    poll(20000)
  );
}

export function useLspNodes() {
  return useSWR<LspNodesResponse>("/api/lsp/nodes", swrFetcher, poll(60000));
}

export function useLspAnalytics() {
  return useSWR<LspAnalytics>("/api/lsp/analytics", swrFetcher, poll(60000));
}

// useLspConfig and useLspAnnounceState are node-scoped (plan GAP #2): the node
// pubkey selects which fleet node to read. Empty node is allowed by lspd for a
// single-node fleet. Config is read once (manual refresh after a save); the
// announce state polls so a publish/self-check result appears.
export function useLspConfig(node: string | undefined) {
  const q = node ? `?node=${encodeURIComponent(node)}` : "";
  return useSWR<LspConfigResponse>(`/api/lsp/config${q}`, swrFetcher);
}

export function useLspAnnounceState(node: string | undefined) {
  const q = node ? `?node=${encodeURIComponent(node)}` : "";
  return useSWR<LspAnnounceState>(
    `/api/lsp/announce-state${q}`,
    swrFetcher,
    poll(30000)
  );
}

// --- mutations (direct request(), not SWR) ---

// updateLspConfig PUTs the (secret-stripped) config object back verbatim plus
// the node selector. lspd re-attaches the node's secrets server-side and
// validates the whole NodeConfig (handlers.go handleUpdateConfig), so the
// caller sends exactly what get_config returned with its safe edits applied.
export function updateLspConfig(
  config: Record<string, unknown>,
  node: string | undefined
) {
  return request<{ status: string }>("/api/lsp/config", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(node ? { config, node } : { config }),
  });
}

export type AnnounceAction = "pause" | "resume" | "unlist";

export function setLspAnnounceState(
  action: AnnounceAction,
  node: string | undefined
) {
  return request<LspAnnounceState>("/api/lsp/announce-state", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(node ? { action, node } : { action }),
  });
}

export function recheckLspAnnouncement(node: string | undefined) {
  return request<LspAnnounceState>("/api/lsp/recheck-announcement", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(node ? { node } : {}),
  });
}
