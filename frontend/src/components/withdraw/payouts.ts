import { millisToLoki } from "src/components/withdraw/withdraw";

// A Cash Hub's on-chain payouts as the operator reads them (WP-19 item 10).
// No operator action changes a payout: NIP-CASH gives the Hub none, and only
// the bill's holder may ask to cancel one (§Cancelling a Payout).

// NIP-CASH §Payout States.
export type PayoutState =
  | "queued"
  | "broadcast"
  | "confirmed"
  | "final"
  | "cancelled";

// GET /api/cash-hubs/:id/payouts.
export type PayoutRow = {
  id: number;
  request_id: string;
  bill_wallet_pubkey: string;
  state: PayoutState;
  address: string;
  amount_millis: number;
  fee_millis: number;
  hub_fee_millis: number;
  network_fee_millis: number;
  payout_millis: number;
  sent_by: number;
  final_depth: number;
  depth: number;
  signed: boolean;
  txid?: string;
  vout?: number;
  held_millis: number;
  cancel_reason?: string;
  cancel_requested_at?: number;
  cancelled_at?: number;
  final_at?: number;
  remint_app_id?: number;
  remint_wallet_pubkey?: string;
  committed_at: number;
};

export type PayoutVersion = {
  seq: number;
  kind: string;
  state: string;
  txid?: string;
  pays: boolean;
  vout?: number;
  inputs: string[];
  vsize?: number;
  fee_sat?: number;
  fee_rate_sat_kvb?: number;
  created_at: number;
  signed_at?: number;
  first_broadcast_at?: number;
  last_broadcast_at?: number;
  confirm_height?: number;
};

export type PayoutFact = {
  at: number;
  state: string;
  txid?: string;
  vout?: number;
  depth?: number;
  miner_fee_millis?: number;
};

// GET /api/cash-hubs/:id/payouts/:payout.
export type PayoutDetail = PayoutRow & {
  versions: PayoutVersion[];
  facts: PayoutFact[];
};

export function stateVariant(
  s: PayoutState
): "secondary" | "outline" | "positive" | "warning" {
  switch (s) {
    case "final":
      return "positive";
    case "confirmed":
    case "broadcast":
      return "outline";
    case "cancelled":
      return "warning";
    default:
      return "secondary";
  }
}

// shorten keeps the head and tail of an address or txid.
export function shorten(s: string, head = 8, tail = 6): string {
  return s.length <= head + tail + 1
    ? s
    : `${s.slice(0, head)}…${s.slice(-tail)}`;
}

// outpoint is the payout's output as txid:vout, absent until a broadcast and
// for a cancelled payout.
export function outpoint(p: Pick<PayoutRow, "txid" | "vout">): string | null {
  if (!p.txid || p.vout == null) {
    return null;
  }
  return `${shorten(p.txid)}:${p.vout}`;
}

// depthLabel is the confirmations so far over final_depth.
export function depthLabel(
  p: Pick<PayoutRow, "depth" | "final_depth" | "txid">
): string {
  return p.txid ? `${p.depth} / ${p.final_depth}` : `— / ${p.final_depth}`;
}

// cancelNote says where a cancellation stands: asked by the holder and not
// yet done, or done with its reason.
export function cancelNote(
  p: Pick<PayoutRow, "state" | "cancel_requested_at" | "cancel_reason">
): { key: "requested" | "cancelled"; reason?: string } | null {
  if (p.state === "cancelled") {
    return { key: "cancelled", reason: p.cancel_reason };
  }
  if (p.cancel_requested_at) {
    return { key: "requested" };
  }
  return null;
}

// overdue: a payout still unbroadcast past its sent_by, which the operator
// should look at.
export function overdue(
  p: Pick<PayoutRow, "state" | "sent_by">,
  nowSecs: number
): boolean {
  return p.state === "queued" && nowSecs > p.sent_by;
}

// feeRate is a version's fee rate in loki per vbyte, from per-1000-vbyte.
export function feeRate(
  v: Pick<PayoutVersion, "fee_rate_sat_kvb">
): string | null {
  if (v.fee_rate_sat_kvb == null) {
    return null;
  }
  return `${(v.fee_rate_sat_kvb / 1000).toFixed(3).replace(/\.?0+$/, "")} loki/vB`;
}

export function loki(millis: number): string {
  return `${millisToLoki(millis)} loki`;
}
