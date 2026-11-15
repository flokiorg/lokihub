// Selling a Cash Hub's cash, the operator side (WP-19 item 8; NIP-CO). The
// operator opens Order connections buyers place orders on, sets the Hub's
// selling terms per payment kind plus the open-order ceilings, and reads the
// orders placed and the payments the Hub kept. Amounts are millis (milli-loki);
// forms take loki with up to three decimals, mirroring the withdraw operator UI.

import { lokiToMillis, MAX_FEE_PPM, millisToLoki } from "src/components/withdraw/withdraw";

// MAX_FEE_PPM_HINT is the ppm ceiling shown in the terms editor's error copy.
export const MAX_FEE_PPM_HINT = MAX_FEE_PPM;

// ---- wire shapes (the Go json tags of cash/cashorder and nmilat/nipCO) ----

// POST/GET /api/cash-hubs/:id/order-connections. connectionString is the signed
// `cashorder1…` string; it is left out when the node cannot sign right now.
export type OrderConnection = {
  id: number;
  hubId: number;
  label: string;
  walletPubkey: string;
  retiredAt?: number;
  createdAt: number;
  connectionString?: string;
};

// nipCO.Tier: one on-chain confirmation tier. The last tier carries no
// up_to_millis and covers every larger value.
export type Tier = {
  up_to_millis?: number;
  n: number;
};

// nipCO.KindTerms: the terms of one payment kind. confirmations is on-chain only.
export type KindTerms = {
  min_millis: number;
  max_millis: number;
  fee_base: number;
  fee_ppm: number;
  window: number;
  confirmations?: Tier[];
};

// nipCO.BillTerms: what the Hub stamps on an ordered bill.
export type BillTerms = {
  redeem_fee_base: number;
  redeem_fee_ppm: number;
  min_transfer_millis: number;
};

// nipCO.Terms: a nil kind is not sold.
export type Terms = {
  lightning?: KindTerms;
  onchain?: KindTerms;
  bill: BillTerms;
};

// cashorder.Settings. GET /api/cash-hubs/:id/order-terms is this or null while
// the Hub sells nothing yet.
export type OrderSettings = {
  terms: Terms;
  openCeilingLightningMillis: number;
  openCeilingOnchainMillis: number;
  openOnchainMax: number;
};

// cashorder.OrderPaymentRow.
export type OrderPaymentRow = {
  state: string;
  value_millis: number;
  confirmations: number;
  address?: string;
  seen_at: number;
  minted_at?: number;
};

// cashorder.OrderRow. payment is the kind, "lightning" or "onchain".
export type OrderRow = {
  id: number;
  state: string;
  payment: string;
  amount_millis: number;
  fee_millis: number;
  pay_millis: number;
  pay_by: number;
  deadline: number;
  confirmations?: number;
  recipient_type: string;
  created_at: number;
  state_at: number;
  payments: OrderPaymentRow[];
};

// cashorder.KeptPaymentRow: a payment the Hub kept with no order.
export type KeptPaymentRow = {
  value_millis: number;
  state: string;
  seen_at: number;
};

// GET /api/cash-hubs/:id/orders.
export type OrdersView = {
  orders: OrderRow[];
  kept: KeptPaymentRow[];
};

// ---- funds-safety guard: H-CO-004 ----

// A buyer places an order on a `cashorder1…` connection string; the Hub's own
// `cashhub1…` pairing is a different credential and must NEVER be offered in the
// sell flow. Both share the "cash" bech32 stem, so an explicit cashhub1 reject
// backs the cashorder1 prefix check.
export const ORDER_CONNECTION_PREFIX = "cashorder1";
export const HUB_CONNECTION_PREFIX = "cashhub1";

// isOrderConnectionString reports a string safe to show in the sell flow: a
// `cashorder1…` string and never the Hub's own `cashhub1…` pairing (H-CO-004).
// The connection card renders a QR or copy only for a string this accepts.
export function isOrderConnectionString(s: string): boolean {
  const v = s.trim().toLowerCase();
  return (
    v.startsWith(ORDER_CONNECTION_PREFIX) && !v.startsWith(HUB_CONNECTION_PREFIX)
  );
}

// ---- Lightning-outbound ceiling check: H-CO-103 ----

// ceilingExceedsOutbound reports an on-chain open-order ceiling that exceeds the
// node's Lightning outbound (spendable) capacity (H-CO-103). An on-chain order
// mints its bill against Lightning liquidity, so a ceiling above what the node
// can spend over Lightning lets the open on-chain orders over-commit it. A zero
// ceiling (on-chain not sold) never warns.
export function ceilingExceedsOutbound(
  openCeilingOnchainMillis: number,
  outboundMillis: number
): boolean {
  return openCeilingOnchainMillis > 0 && openCeilingOnchainMillis > outboundMillis;
}

// ---- badge variants for the states nipCO/lokihub define ----

export type BadgeVariant =
  | "default"
  | "secondary"
  | "outline"
  | "positive"
  | "warning"
  | "destructive";

// orderStateVariant colours an order's state (NIP-CO §Order State).
export function orderStateVariant(state: string): BadgeVariant {
  switch (state) {
    case "minted":
      return "positive";
    case "paid":
      return "outline";
    case "expired":
    case "closed":
      return "warning";
    default:
      return "secondary";
  }
}

// paymentStateVariant colours one payment's state (NIP-CO §Paying and Minting).
export function paymentStateVariant(state: string): BadgeVariant {
  switch (state) {
    case "minted":
      return "positive";
    case "confirmed":
      return "outline";
    case "reorged":
      return "warning";
    case "dropped":
      return "destructive";
    default:
      return "secondary";
  }
}

// loki formats a millis amount as a loki amount with its unit.
export function loki(millis: number, unitName = "loki"): string {
  return `${millisToLoki(millis)} ${unitName}`;
}

// ---- the terms editor form ----

export type KindForm = {
  enabled: boolean;
  minLoki: string;
  maxLoki: string;
  feeBaseLoki: string;
  feePpm: string;
  window: string;
};

export type TierForm = { upToLoki: string; n: string };

export type TermsForm = {
  lightning: KindForm;
  onchain: KindForm;
  tiers: TierForm[];
  redeemFeeBaseLoki: string;
  redeemFeePpm: string;
  minTransferLoki: string;
  ceilingLightningLoki: string;
  ceilingOnchainLoki: string;
  openOnchainMax: string;
};

export type TermsFormError =
  | "lightningMin"
  | "lightningMax"
  | "lightningFeeBase"
  | "lightningFeePpm"
  | "lightningWindow"
  | "lightningMinAboveMax"
  | "lightningCeiling"
  | "onchainMin"
  | "onchainMax"
  | "onchainFeeBase"
  | "onchainFeePpm"
  | "onchainWindow"
  | "onchainMinAboveMax"
  | "onchainWhole"
  | "onchainCeiling"
  | "onchainCeilingWhole"
  | "tiers"
  | "redeemFeeBase"
  | "redeemFeePpm"
  | "minTransfer"
  | "openOnchainMax";

const emptyKind = (): KindForm => ({
  enabled: false,
  minLoki: "",
  maxLoki: "",
  feeBaseLoki: "",
  feePpm: "",
  window: "",
});

// emptyTermsForm is the "not selling yet" initial state (null terms): both kinds
// off, one open on-chain tier so the editor has a row to fill.
export function emptyTermsForm(): TermsForm {
  return {
    lightning: emptyKind(),
    onchain: emptyKind(),
    tiers: [{ upToLoki: "", n: "1" }],
    redeemFeeBaseLoki: "",
    redeemFeePpm: "",
    minTransferLoki: "",
    ceilingLightningLoki: "",
    ceilingOnchainLoki: "",
    openOnchainMax: "0",
  };
}

function kindForm(k: KindTerms | undefined): KindForm {
  if (!k) {
    return emptyKind();
  }
  return {
    enabled: true,
    minLoki: millisToLoki(k.min_millis),
    maxLoki: millisToLoki(k.max_millis),
    feeBaseLoki: millisToLoki(k.fee_base),
    feePpm: String(k.fee_ppm),
    window: String(k.window),
  };
}

// formFromSettings reads stored settings into the editor form; null settings
// give the "not selling yet" initial state.
export function formFromSettings(s: OrderSettings | null): TermsForm {
  if (!s) {
    return emptyTermsForm();
  }
  const tiers = (s.terms.onchain?.confirmations ?? []).map((tr) => ({
    upToLoki: tr.up_to_millis == null ? "" : millisToLoki(tr.up_to_millis),
    n: String(tr.n),
  }));
  return {
    lightning: kindForm(s.terms.lightning),
    onchain: kindForm(s.terms.onchain),
    tiers: tiers.length ? tiers : [{ upToLoki: "", n: "1" }],
    redeemFeeBaseLoki: millisToLoki(s.terms.bill.redeem_fee_base),
    redeemFeePpm: String(s.terms.bill.redeem_fee_ppm),
    minTransferLoki: millisToLoki(s.terms.bill.min_transfer_millis),
    ceilingLightningLoki: millisToLoki(s.openCeilingLightningMillis),
    ceilingOnchainLoki: millisToLoki(s.openCeilingOnchainMillis),
    openOnchainMax: String(s.openOnchainMax),
  };
}

function ppm(text: string): number | null {
  const v = text.trim() === "" ? "0" : text.trim();
  if (!/^\d+$/.test(v) || Number(v) > MAX_FEE_PPM) {
    return null;
  }
  return Number(v);
}

function posInt(text: string): number | null {
  const v = text.trim();
  if (!/^\d+$/.test(v) || Number(v) <= 0) {
    return null;
  }
  return Number(v);
}

// parseKind reads one payment kind's fields; prefix names its error keys.
function parseKind(
  f: KindForm,
  onchain: boolean
): { kind?: KindTerms; error?: TermsFormError } {
  const p = (name: string) =>
    (onchain ? `onchain${name}` : `lightning${name}`) as TermsFormError;
  const min = lokiToMillis(f.minLoki);
  if (min === null) {
    return { error: p("Min") };
  }
  const max = lokiToMillis(f.maxLoki);
  if (max === null) {
    return { error: p("Max") };
  }
  if (min > max) {
    return { error: p("MinAboveMax") };
  }
  if (onchain && (min % 1000 !== 0 || max % 1000 !== 0)) {
    return { error: "onchainWhole" };
  }
  const feeBase = lokiToMillis(f.feeBaseLoki === "" ? "0" : f.feeBaseLoki);
  if (feeBase === null) {
    return { error: p("FeeBase") };
  }
  const feePpm = ppm(f.feePpm);
  if (feePpm === null) {
    return { error: p("FeePpm") };
  }
  const window = posInt(f.window);
  if (window === null) {
    return { error: p("Window") };
  }
  return {
    kind: {
      min_millis: min,
      max_millis: max,
      fee_base: feeBase,
      fee_ppm: feePpm,
      window,
    },
  };
}

// parseTiers builds the on-chain confirmation tiers: each n a positive integer,
// up_to_millis ascending with only the last tier open (NIP-CO §Terms).
export function parseTiers(
  rows: TierForm[]
): { tiers?: Tier[]; error?: "tiers" } {
  const tiers: Tier[] = [];
  let prev: number | null = null;
  for (let i = 0; i < rows.length; i++) {
    const last = i === rows.length - 1;
    const n = posInt(rows[i].n);
    if (n === null) {
      return { error: "tiers" };
    }
    if (last) {
      if (rows[i].upToLoki.trim() !== "") {
        return { error: "tiers" };
      }
      tiers.push({ n });
      continue;
    }
    const upTo = lokiToMillis(rows[i].upToLoki);
    if (upTo === null || upTo % 1000 !== 0 || (prev !== null && upTo <= prev)) {
      return { error: "tiers" };
    }
    prev = upTo;
    tiers.push({ up_to_millis: upTo, n });
  }
  return { tiers };
}

// parseTermsForm checks the editor form as the Hub does (refused, never
// clamped) for everything it can see locally — amount and fee formats, min ≤
// max, ppm ≤ 1000000, whole base units on-chain, a required positive ceiling
// per enabled kind, ascending confirmation tiers — and leaves the deeper bounds
// (the per-wallet funding ceiling, the window range, the split floor) to the
// PUT, which refuses and reports them. A kind that is off is simply not sold.
export function parseTermsForm(f: TermsForm): {
  settings?: OrderSettings;
  error?: TermsFormError;
} {
  const terms: Terms = {
    bill: { redeem_fee_base: 0, redeem_fee_ppm: 0, min_transfer_millis: 0 },
  };
  let ceilingLn = 0;
  let ceilingOnchain = 0;

  if (f.lightning.enabled) {
    const r = parseKind(f.lightning, false);
    if (r.error || !r.kind) {
      return { error: r.error };
    }
    terms.lightning = r.kind;
    const c = lokiToMillis(f.ceilingLightningLoki);
    if (c === null || c <= 0) {
      return { error: "lightningCeiling" };
    }
    ceilingLn = c;
  }

  if (f.onchain.enabled) {
    const r = parseKind(f.onchain, true);
    if (r.error || !r.kind) {
      return { error: r.error };
    }
    const tiers = parseTiers(f.tiers);
    if (tiers.error || !tiers.tiers) {
      return { error: "tiers" };
    }
    terms.onchain = { ...r.kind, confirmations: tiers.tiers };
    const c = lokiToMillis(f.ceilingOnchainLoki);
    if (c === null || c <= 0) {
      return { error: "onchainCeiling" };
    }
    if (c % 1000 !== 0) {
      return { error: "onchainCeilingWhole" };
    }
    ceilingOnchain = c;
  }

  const redeemFeeBase = lokiToMillis(
    f.redeemFeeBaseLoki === "" ? "0" : f.redeemFeeBaseLoki
  );
  if (redeemFeeBase === null) {
    return { error: "redeemFeeBase" };
  }
  const redeemFeePpm = ppm(f.redeemFeePpm);
  if (redeemFeePpm === null) {
    return { error: "redeemFeePpm" };
  }
  const minTransfer = lokiToMillis(
    f.minTransferLoki === "" ? "0" : f.minTransferLoki
  );
  if (minTransfer === null) {
    return { error: "minTransfer" };
  }
  terms.bill = {
    redeem_fee_base: redeemFeeBase,
    redeem_fee_ppm: redeemFeePpm,
    min_transfer_millis: minTransfer,
  };

  const openMaxText = f.openOnchainMax.trim() === "" ? "0" : f.openOnchainMax.trim();
  if (!/^\d+$/.test(openMaxText)) {
    return { error: "openOnchainMax" };
  }

  return {
    settings: {
      terms,
      openCeilingLightningMillis: ceilingLn,
      openCeilingOnchainMillis: ceilingOnchain,
      openOnchainMax: Number(openMaxText),
    },
  };
}
