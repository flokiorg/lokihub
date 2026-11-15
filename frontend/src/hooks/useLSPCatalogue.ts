import useSWR from "swr";

import { request } from "src/utils/request";
import { swrFetcher } from "src/utils/swr";

export type LSPFeeRow = {
  MinFeeMillis: number;
  Proportional: number;
  MinLifetime: number;
  MaxClientToSelfDelay: number;
  MinPaymentSizeMillis: number;
  MaxPaymentSizeMillis: number;
};

export type LSPOption = { Name: string; Value: string };

export type LSPFieldDiff = {
  Field: string;
  Announced: string;
  Live: string;
  Verdict: number;
};

export type LSPLiveCheck = {
  at: number;
  nodeId: string;
  addr: string;
  error?: string;
  usable: boolean;
  worse: boolean;
  missingProtocol?: number[];
  changed?: LSPFieldDiff[];
};

// LSPCatalogueEntry is one LSP of GET /api/lsps (WP-26). name, description and
// website are the LSP's claims; inGraph, graphChannels and graphCapacity are
// this node's own view of the network.
export type LSPCatalogueEntry = {
  nodeKey: string;
  pubkey: string;
  host: string;
  name: string;
  description: string;
  website: string;
  image: string;
  chain: string;
  network: string;
  status: "open" | "paused" | "closed" | "";
  operatorPubkey: string;
  announcedAt: number;
  expiresAt: number;
  usable: boolean;
  invalidReason: string;
  inGraph: boolean;
  graphChannels: number;
  graphCapacity: number;
  trusted: boolean;
  lastCheckAt: number;
  addrs: string[] | null;
  protocols: number[] | null;
  lsps1: LSPOption[] | null;
  lsps2Fees: LSPFeeRow[] | null;
  lsps5: LSPOption[] | null;
  languages: string[] | null;
  relays: string[] | null;
  lastCheck?: LSPLiveCheck;
};

export function useLSPCatalogue() {
  return useSWR<LSPCatalogueEntry[]>("/api/lsps", swrFetcher);
}

export function trustLSP(nodeKey: string, trusted: boolean) {
  return request<LSPCatalogueEntry>(`/api/lsps/${nodeKey}/trust`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ trusted }),
  });
}

export function checkLSP(nodeKey: string) {
  return request<LSPCatalogueEntry>(`/api/lsps/${nodeKey}/check`, {
    method: "POST",
  });
}

export function addLSP(pubkey: string) {
  return request<LSPCatalogueEntry>("/api/lsps", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ pubkey }),
  });
}
