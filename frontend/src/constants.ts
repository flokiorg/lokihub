export const localStorageKeys = {
  returnTo: "returnTo",
  setupReturnTo: "setupReturnTo",
  channelOrder: "channelOrder",
  authToken: "authToken",
  supportLokiSidebarHintHiddenUntil: "supportLokiSidebarHintHiddenUntil",
  appAlertsHiddenUntil: "appAlertsHiddenUntil",
  lokihubLang: "lokihub-lang",
  preferredInputUnit: "lokihub-preferred-input-unit",
};

export const ONCHAIN_DUST_LOKI = 1000;
export const LOKI_HIDE_HOSTED_BALANCE_BELOW = 100;
export const LOKI_MIN_HOSTED_BALANCE_FOR_FIRST_CHANNEL = 10_000;

export const LIST_TRANSACTIONS_LIMIT = 20;
export const LIST_APPS_LIMIT = 20;
export const LIST_CIRCLE_CHILDREN_LIMIT = 20;
export const LIST_CIRCLE_ALLOWLIST_LIMIT = 20;
export const LIST_JIT_ALLOCATIONS_LIMIT = 20;


export const SUBWALLET_APPSTORE_APP_ID = "lokies";
export const LOKI_ACCOUNT_APP_NAME = "loki-account";

export const DEFAULT_APP_BUDGET_LOKI = 21 * 100_000_000; // 21 FLC — matches the first FLC preset
export const DEFAULT_APP_BUDGET_RENEWAL = "monthly";

export const FLOKICOIN_DISPLAY_FORMAT_FLC = "flc";
export const FLOKICOIN_DISPLAY_FORMAT_LOKI = "loki";
export const FLOKICOIN_DISPLAY_FORMAT_AUTO = "auto";

// WEEK_SCALE_PRESETS is for DurationInput callers on a longer timescale than
// JIT's hour/day-scale default (e.g. a Circle Hub's max wallet expiry).
export const WEEK_SCALE_PRESETS: { label: string; seconds: number }[] = [
  { label: "1 week", seconds: 7 * 86400 },
  { label: "1 month", seconds: 30 * 86400 },
  { label: "3 months", seconds: 90 * 86400 },
  { label: "6 months", seconds: 180 * 86400 },
  { label: "1 year", seconds: 365 * 86400 },
];
