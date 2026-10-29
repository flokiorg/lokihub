// On-chain withdrawals, the operator side (WP-19 item 10; NIP-CASH
// §Redeeming On-chain). The operator sets only the node switch and, per Cash
// Hub, its own enable, withdraw_fee_base (millis), withdraw_fee_ppm and an
// optional minimum and maximum. Script types and final_depth are derived and
// shown read-only; the network fee is estimated live per address type.

// GET/PUT /api/cash-hubs/:id/withdraw-terms.
export type WithdrawTermsInput = {
  enabled: boolean;
  fee_base_millis: number;
  fee_ppm: number;
  min_millis?: number;
  max_millis?: number;
};

export type WithdrawTermsView = WithdrawTermsInput & {
  script_types: [number, number][];
  final_depth: number;
  node_enabled: boolean;
  capable: boolean;
  offered: boolean;
};

// GET /api/cash-withdraw/node.
export type WithdrawNodeState = { enabled: boolean; capable: boolean };

// GET /api/cash-withdraw/status: the payout engine's health.
export type WithdrawEngineStatus = {
  ready: boolean;
  payouts: Record<string, number> | null;
  unconfirmed_millis: number;
  set_aside_sat: number;
  eligible_sat: number;
  floor_sat: number;
  oldest_unbroadcast_secs: number;
  bumps_total: number;
  reorgs_total: number;
  invalidations_total: number;
  coin_vanished_total: number;
  invariant_failures_total: number;
};

// The per-hub form: amounts in loki with up to three decimals (millis).
export type WithdrawForm = {
  enabled: boolean;
  feeBaseLoki: string;
  feePpm: string;
  minLoki: string;
  maxLoki: string;
};

export type WithdrawFormError =
  | "feeBase"
  | "feePpm"
  | "min"
  | "max"
  | "minAboveMax";

export const MAX_FEE_PPM = 1_000_000;

// lokiToMillis reads a non-negative loki amount with at most three decimals;
// null when it is not one.
export function lokiToMillis(s: string): number | null {
  const v = s.trim();
  const m = /^(\d+)(?:\.(\d{1,3}))?$/.exec(v);
  if (!m) {
    return null;
  }
  const millis = Number(m[1]) * 1000 + Number((m[2] ?? "").padEnd(3, "0"));
  return Number.isSafeInteger(millis) ? millis : null;
}

export function millisToLoki(millis: number): string {
  const whole = Math.floor(millis / 1000);
  const frac = millis % 1000;
  if (frac === 0) {
    return String(whole);
  }
  return `${whole}.${String(frac).padStart(3, "0").replace(/0+$/, "")}`;
}

export function formFromView(v: WithdrawTermsView): WithdrawForm {
  return {
    enabled: v.enabled,
    feeBaseLoki: millisToLoki(v.fee_base_millis),
    feePpm: String(v.fee_ppm),
    minLoki: v.min_millis == null ? "" : millisToLoki(v.min_millis),
    maxLoki: v.max_millis == null ? "" : millisToLoki(v.max_millis),
  };
}

// parseWithdrawForm checks the form as the Hub does (refused, never
// clamped): the fee base and bounds are non-negative, the ppm is a whole
// number of at most 1000000, and a minimum does not exceed the maximum. An
// empty minimum or maximum means none.
export function parseWithdrawForm(f: WithdrawForm): {
  input?: WithdrawTermsInput;
  error?: WithdrawFormError;
} {
  const feeBase = lokiToMillis(f.feeBaseLoki === "" ? "0" : f.feeBaseLoki);
  if (feeBase === null) {
    return { error: "feeBase" };
  }
  const ppmText = f.feePpm.trim() === "" ? "0" : f.feePpm.trim();
  if (!/^\d+$/.test(ppmText) || Number(ppmText) > MAX_FEE_PPM) {
    return { error: "feePpm" };
  }
  const input: WithdrawTermsInput = {
    enabled: f.enabled,
    fee_base_millis: feeBase,
    fee_ppm: Number(ppmText),
  };
  if (f.minLoki.trim() !== "") {
    const min = lokiToMillis(f.minLoki);
    if (min === null) {
      return { error: "min" };
    }
    input.min_millis = min;
  }
  if (f.maxLoki.trim() !== "") {
    const max = lokiToMillis(f.maxLoki);
    if (max === null) {
      return { error: "max" };
    }
    input.max_millis = max;
  }
  if (
    input.min_millis !== undefined &&
    input.max_millis !== undefined &&
    input.min_millis > input.max_millis
  ) {
    return { error: "minAboveMax" };
  }
  return { input };
}

// hubFeeMillis is NIP-CASH §The Withdraw Fee's Hub share for an amount, the
// network fee excluded: base + floor(ppm × amount / 1000000).
export function hubFeeMillis(
  input: Pick<WithdrawTermsInput, "fee_base_millis" | "fee_ppm">,
  amountMillis: number
): number {
  return (
    input.fee_base_millis + Math.floor((input.fee_ppm * amountMillis) / 1e6)
  );
}

// scriptTypeName names a [witness_version, program_length] pair.
export function scriptTypeName([version, length]: [number, number]): string {
  if (version === 0 && length === 20) {
    return "P2WPKH";
  }
  if (version === 0 && length === 32) {
    return "P2WSH";
  }
  if (version === 1 && length === 32) {
    return "P2TR";
  }
  return `v${version}/${length}`;
}

// hubEnableBlocked is why a hub cannot turn withdrawals on, if it cannot.
export function hubEnableBlocked(
  v: Pick<WithdrawTermsView, "node_enabled" | "capable">
): "nodeOff" | "notCapable" | null {
  if (!v.capable) {
    return "notCapable";
  }
  if (!v.node_enabled) {
    return "nodeOff";
  }
  return null;
}

export type StatusKey =
  | "eligible"
  | "setAside"
  | "floor"
  | "unconfirmed"
  | "oldestUnbroadcast"
  | "payouts"
  | "bumps"
  | "reorgs"
  | "invalidations"
  | "coinsVanished"
  | "invariantFailures";

// statusRows are the status panel's lines, as an i18n key and a value with
// its unit; a non-zero failure counter is flagged.
export function statusRows(
  st: WithdrawEngineStatus
): { key: StatusKey; value: string; alert?: boolean }[] {
  const payouts = Object.entries(st.payouts ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([state, n]) => `${state} ${n}`)
    .join(", ");
  return [
    { key: "eligible", value: `${st.eligible_sat} loki` },
    { key: "setAside", value: `${st.set_aside_sat} loki` },
    { key: "floor", value: `${st.floor_sat} loki` },
    {
      key: "unconfirmed",
      value: `${millisToLoki(st.unconfirmed_millis)} loki`,
    },
    { key: "oldestUnbroadcast", value: `${st.oldest_unbroadcast_secs} s` },
    { key: "payouts", value: payouts || "0" },
    { key: "bumps", value: String(st.bumps_total) },
    { key: "reorgs", value: String(st.reorgs_total) },
    { key: "invalidations", value: String(st.invalidations_total) },
    {
      key: "coinsVanished",
      value: String(st.coin_vanished_total),
      alert: st.coin_vanished_total > 0,
    },
    {
      key: "invariantFailures",
      value: String(st.invariant_failures_total),
      alert: st.invariant_failures_total > 0,
    },
  ];
}
