// TypeScript shapes for the lspd dashboard JSON-RPC results, as the Hub proxies
// them verbatim through /api/lsp/* (plan §2.4, §3.3). These are REPLICATED from
// lspd's dashboard handlers (lspd/dashboard/handlers.go, announce_state.go,
// node_select.go) — never imported — so lspd can be re-versioned without
// touching the frontend. Keep them in sync with those handlers' response JSON.

// get_summary (handlers.go SummaryResponse). Liquidity and revenue are in loki.
export interface LspSummary {
  node_name: string;
  node_pubkey: string;
  node_host: string;
  version: string;
  revenue_24h: number;
  liquidity_total: number;
  liquidity_free: number;
  liquidity_blocked: number;
  liquidity_outbound: number;
  liquidity_inbound: number;
  active_orders: Record<string, number>;
  node_status: string;
  synced_to_chain: boolean;
  sync_height: number;
  peers_connected: number;
  channels_active: number;
  channels_pending: number;
  channels_inactive: number;
  revenue_trend: string;
}

// list_orders (core.Lsps1Order). Amounts are in loki; `amount`/`fee` are the
// order total and fee (the Go json tags remap OrderTotalLoki/FeeTotalLoki).
export interface LspOrder {
  order_id: string;
  peer_id: string;
  peer_alias: string;
  peer_color: string;
  state: string;
  payment_state: string;
  payment_invoice: string;
  lsp_balance_loki: number;
  client_balance_loki: number;
  amount: number;
  fee: number;
  announce_channel: boolean;
  channel_expiry_blocks: number;
  funding_confirms_within_blocks: number;
  required_channel_confirmations: number;
  funding_txid: string | null;
  funding_outpoint: number | null;
  refund_onchain_address: string;
  token: string;
  created_at: string;
  expires_at: string;
  reserved_amount_loki: number;
}

// get_order_detail (handlers.go handleGetOrderDetail).
export interface LspOrderTimelineEvent {
  event: string;
  // Absent for state transitions whose exact time the LSP does not record yet.
  timestamp?: string;
  detail: string;
}
export interface LspOrderDetail {
  order: LspOrder;
  timeline: LspOrderTimelineEvent[];
}

// list_peers (handlers.go handleListPeers). in_loki/out_loki are received-from
// and sent-to totals.
export interface LspPeer {
  pub_key: string;
  alias: string;
  color: string;
  address: string;
  inbound: boolean;
  active: boolean;
  in_loki: number;
  out_loki: number;
  order_count: number;
}

// list_jit_channels (handlers.go handleListJitChannels).
export interface LspJitChannel {
  order_id: string;
  channel_id: string;
  peer_id: string;
  capacity: number;
  fee: number;
  status: string;
  created_at: string;
  txid: string;
}

// list_nodes (node_select.go NodeView). node_id is the 66-hex node pubkey used
// as the per-node selector for config/announce methods.
export interface LspNode {
  node_id: string;
  name: string;
}
export interface LspNodesResponse {
  nodes: LspNode[];
}

// get_analytics (handlers.go handleGetAnalytics).
export interface LspAnalyticsPoint {
  date: string;
  day: string;
  revenue: number;
  count: number;
  volume: number;
}
export interface LspAnalyticsSlice {
  name: string;
  value: number;
  color: string;
}
export interface LspAnalytics {
  total_revenue: number;
  success_rate: number;
  avg_order_size: number;
  active_peers: number;
  revenue_history: LspAnalyticsPoint[];
  order_distribution: LspAnalyticsSlice[];
}

// get_config (handlers.go handleGetConfig). The config object is the node's
// NodeConfig with secret fields (flspdPrivateKey/tokens/flnd) already stripped
// server-side; it is treated as an opaque record here and the secret keys are
// never rendered defensively. Numeric NodeConfig fields are json:",string", so
// they arrive as strings.
export type LspConfigObject = Record<string, unknown>;
export interface LspConfigResponse {
  config: LspConfigObject;
  announce_relays: string[];
}

// get_announce_state / set_announce_state (announce_state.go
// AnnounceStateResponse).
export interface LspAnnounceRelay {
  relay: string;
  accepted: boolean;
  error?: string;
  retry_at?: string;
}
export interface LspAnnounceEvent {
  id: string;
  created_at: string;
  expiration: string;
  status: string;
  relays: LspAnnounceRelay[];
}
export interface LspAnnounceSelfCheck {
  checked_at: string;
  ok: boolean;
  problems: string[];
}
export interface LspAnnouncePublisher {
  last_event: LspAnnounceEvent | null;
  self_check: LspAnnounceSelfCheck | null;
  blocked: string;
  last_error: string;
}
export interface LspAnnounceState {
  node_pubkey: string;
  enabled: boolean;
  status: string;
  updated_at: string | null;
  relays: string[];
  publisher: LspAnnouncePublisher | null;
}

// Config keys that must never be rendered even if a future lspd stopped
// stripping them (the dashboard strips flspdPrivateKey/tokens/flnd today —
// handlers.go configSecretFields / C-1). Defensive belt-and-braces.
export const LSP_CONFIG_SECRET_KEYS = new Set([
  "flspdPrivateKey",
  "tokens",
  "flnd",
]);

// formatLoki renders a loki integer with thousands separators. lspd amounts are
// already whole loki (not mloki), so they are shown as-is rather than routed
// through the Hub's mloki formatter, which would misread the unit.
export function formatLoki(value: number | undefined | null): string {
  if (value === undefined || value === null || Number.isNaN(value)) {
    return "0 loki";
  }
  return `${value.toLocaleString()} loki`;
}

// A small palette for LSPS1 order/payment states, mapped onto the shadcn Badge
// variants the app already uses.
export type BadgeVariant =
  | "default"
  | "secondary"
  | "destructive"
  | "outline"
  | "positive"
  | "warning";

export function orderStateVariant(state: string): BadgeVariant {
  switch (state) {
    case "COMPLETED":
      return "positive";
    case "PAID":
      return "default";
    case "CREATED":
      return "secondary";
    case "FAILED":
      return "destructive";
    case "EXPIRED":
      return "outline";
    default:
      return "outline";
  }
}
