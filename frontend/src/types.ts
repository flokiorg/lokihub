import {
    BellIcon,
    CirclePlusIcon,
    CrownIcon,
    GiftIcon,
    HandCoinsIcon,
    InfoIcon,
    LucideIcon,
    NetworkIcon,
    NotebookTabsIcon,
    PenLineIcon,
    SearchIcon,
    UsersIcon,
    WalletMinimalIcon,
} from "lucide-react";

export type BackendType = "FLND";

export type Nip47RequestMethod =
  | "get_info"
  | "get_balance"
  | "get_budget"
  | "make_invoice"
  | "pay_invoice"
  | "pay_keysend"
  | "lookup_invoice"
  | "list_transactions"
  | "sign_message"
  | "multi_pay_invoice"
  | "multi_pay_keysend"
  | "make_hold_invoice"
  | "settle_hold_invoice"
  | "cancel_hold_invoice";

export type BudgetRenewalType =
  | "daily"
  | "weekly"
  | "monthly"
  | "yearly"
  | "never"
  | "";

export type Scope =
  | "pay_invoice" // also used for pay_keysend, multi_pay_invoice, multi_pay_keysend
  | "get_balance"
  | "get_info"
  | "make_invoice"
  | "lookup_invoice"
  | "list_transactions"
  | "sign_message"
  | "notifications" // covers all notification types
  | "superuser"
  | "jit_hub"
  | "circle_wallet"
  | "jit_claim_funds";

export type Nip47NotificationType = "payment_received" | "payment_sent";

export type ScopeIconMap = {
  [key in Scope]: LucideIcon;
};

export const scopeIconMap: ScopeIconMap = {
  get_balance: WalletMinimalIcon,
  get_info: InfoIcon,
  list_transactions: NotebookTabsIcon,
  lookup_invoice: SearchIcon,
  make_invoice: CirclePlusIcon,
  pay_invoice: HandCoinsIcon,
  sign_message: PenLineIcon,
  notifications: BellIcon,
  superuser: CrownIcon,
  jit_hub: NetworkIcon,
  circle_wallet: UsersIcon,
  jit_claim_funds: GiftIcon,
};

export type WalletCapabilities = {
  methods: Nip47RequestMethod[];
  scopes: Scope[];
  notificationTypes: Nip47NotificationType[];
};

export const validBudgetRenewals: BudgetRenewalType[] = [
  "daily",
  "weekly",
  "monthly",
  "yearly",
  "never",
];

export const scopeDescriptions: Record<Scope, string> = {
  get_balance: "Read your balance",
  get_info: "Read your node info",
  list_transactions: "Read transaction history",
  lookup_invoice: "Lookup status of invoices",
  make_invoice: "Create invoices",
  pay_invoice: "Send payments",
  sign_message: "Sign messages",
  notifications: "Receive wallet notifications",
  superuser: "Create other app connections",
  jit_hub: "Issue spend-only wallets on demand to beneficiaries",
  circle_wallet: "Issue wallets to your circle's members",
  jit_claim_funds: "Claim your allocated share of a shared JIT wallet",
};

export const expiryOptions: Record<string, number> = {
  "1 week": 7,
  "1 month": 30,
  "1 year": 365,
  Never: 0,
};

export const budgetOptions: Record<string, number> = {
  "21": 21_000,
  "500": 500_000,
  "21M": 21_000_000,
  Unlimited: 0,
};

export interface ErrorResponse {
  message: string;
}

export interface App {
  id: number;
  name: string;
  description: string;
  appPubkey: string;
  uniqueWalletPubkey: boolean;
  walletPubkey: string;
  createdAt: string;
  updatedAt: string;
  lastUsedAt?: string;
  expiresAt?: string;
  isolated: boolean;
  kind?: string;
  balance: number;

  scopes: Scope[];
  maxAmount: number;
  budgetUsage: number;
  budgetRenewal: BudgetRenewalType;
  metadata?: AppMetadata;
  // circleIdentity is set only for circle_hub apps — a lightweight
  // summary of the attached (possibly shared) identity plus policy-specific
  // counts, so the Circles card doesn't need an extra round-trip per app.
  // followingCount is undefined (not 0) when not yet known — the backend
  // only ever returns it from a non-blocking cache peek, never a live fetch,
  // so a brand-new following-policy identity should render as still-loading
  // rather than "0 following" until the periodic refresher populates it.
  circleIdentity?: CircleIdentitySummary & {
    followingCount?: number;
    allowlistCount: number;
    // policySyncedAt is "last policy update" — when membership was last
    // confirmed from its source of truth (relay fetch for "following", last
    // allowlist add/remove for "allowlist"). Undefined (not "never") when not
    // yet known, same loading semantics as followingCount.
    policySyncedAt?: string;
  };
  // jitPerWalletMaxMloki/jitMaxExpSecs are set only for jit_hub apps — the
  // hub-wide defaults set at creation time, editable from Edit Connection.
  jitPerWalletMaxMloki?: number;
  jitMaxExpSecs?: number;
  // circleMaxExpSecs/circleFeesPpm/circlePerWalletMaxMloki/circleMinBudgetRenewal
  // are set only for circle_hub apps, for the same reason as above.
  circleMaxExpSecs?: number;
  circleFeesPpm?: number;
  circlePerWalletMaxMloki?: number;
  circleMinBudgetRenewal?: BudgetRenewalType;
}

export interface CircleIdentitySummary {
  id: number;
  name: string;
  policy: "following" | "allowlist";
  providerPubkey: string;
  // How many circle_hub apps currently reference this identity — an
  // identity with usedByCount > 0 can't be deleted until those are removed.
  usedByCount: number;
}

export interface CircleIdentityResponse extends CircleIdentitySummary {
  followingCount?: number;
  allowlistCount: number;
  allowlistPubkeys?: string[];
  usedByCount: number;
}

// CircleRefreshPreview is the delta a following-policy allowlist refresh
// would apply, plus the full freshly-fetched list — confirming applies
// `pubkeys` directly rather than re-fetching from relays a second time, so
// what's applied always matches exactly what was shown for review.
export interface CircleRefreshPreview {
  pubkeys: string[];
  added: string[];
  removed: string[];
}

export interface AppPermissions {
  scopes: Scope[];
  maxAmount: number;
  budgetRenewal: BudgetRenewalType;
  expiresAt?: Date;
  isolated: boolean;
  // jitHub/jitPerWalletMaxLoki/jitMaxExpSecs are only meaningful for a new
  // connection (isNewConnection) — they select kind "jit_hub" instead of
  // "isolated" at creation time and are never editable afterward here (see
  // AppDetails' dedicated Hub Settings card for that).
  jitHub?: boolean;
  jitPerWalletMaxLoki?: number;
  jitMaxExpSecs?: number;
}

export interface LSP {
  name: string;
  pubkey: string;
  host: string;
  active: boolean;
  isCommunity?: boolean;
  description?: string;
  website?: string;
}

export interface InfoResponse {
  backendType: BackendType;
  setupCompleted: boolean;
  running: boolean;
  network?: Network;
  version: string;
  relays: { url: string; online: boolean }[];
  unlocked: boolean;
  startupState: string;
  startupError: string;
  startupErrorTime: string;
  autoUnlockPasswordSupported: boolean;
  autoUnlockPasswordEnabled: boolean;
  currency: string;
  nodeAlias: string;
  mempoolUrl: string;
  flokicoinDisplayFormat: FlokicoinDisplayFormat;
  lokihubServicesURL: string;
  swapServiceUrl: string;
  messageboardNwcUrl: string;
  relay: string;
  generalRelay: string;
  searchRelay: string;
  lsps: LSP[];
  enableSwap: boolean;
  enableMessageboardNwc: boolean;
  workDir: string;
  enablePolling?: boolean;
}

export type FlokicoinDisplayFormat = "loki" | "flc" | "auto";

export type HealthAlarmKind =
  | "node_not_ready"
  | "channels_offline"
  | "nostr_relay_offline"
  | "vss_no_subscription";

export type HealthAlarm = {
  kind: HealthAlarmKind;
  rawDetails?: unknown;
};

export type HealthResponse = {
  alarms: HealthAlarm[];
  message?: string;
};

export type Network = "flokicoin" | "testnet" | "signet";

export type AppMetadata = {
  app_store_app_id?: string;
  lud16?: string;
  // Full nostr pubkey behind a JIT/circle wallet's identity, stored for
  // display purposes (resolving a profile name instead of the npub prefix
  // baked into the app name). "identity_pubkey" for JIT wallets (pubkey mode
  // at creation, connection_key mode once claimed), "requester_pubkey" for
  // circle wallets.
  identity_pubkey?: string;
  requester_pubkey?: string;
} & Record<string, unknown>;

export type AutoSwapConfig = {
  type: "out";
  enabled: boolean;
  balanceThreshold: number;
  swapAmount: number;
  destination: string;
};

export type SwapInfo = {
  lokiServiceFee: number;
  boltzServiceFee: number;
  boltzNetworkFee: number;
  minAmount: number;
  maxAmount: number;
};

export type BaseSwap = {
  id: string;
  sendAmount: number;
  lockupAddress: string;
  paymentHash: string;
  invoice: string;
  autoSwap: boolean;
  usedXpub: boolean;
  boltzPubkey: string;
  createdAt: string;
  updatedAt: string;
  lockupTxId?: string;
  claimTxId?: string;
  receiveAmount?: number;
};

export type SwapIn = BaseSwap & {
  type: "in";
  state: "PENDING" | "SUCCESS" | "FAILED" | "REFUNDED";
  refundAddress?: string;
};

export type SwapOut = BaseSwap & {
  type: "out";
  state: "PENDING" | "SUCCESS" | "FAILED";
  destinationAddress: string;
};

export type Swap = SwapIn | SwapOut;

export type SwapResponse = {
  swapId: string;
  paymentHash: string;
};

export interface MnemonicResponse {
  mnemonic: string;
}

export interface CreateAppRequest {
  name: string;
  pubkey?: string;
  maxAmount?: number;
  budgetRenewal?: BudgetRenewalType;
  expiresAt?: string;
  scopes: Scope[];
  returnTo?: string;
  kind?: string;
  jitPerWalletMaxMloki?: number;
  jitMaxExpSecs?: number;
  circleMaxExpSecs?: number;
  circleFeesPpm?: number;
  circlePerWalletMaxMloki?: number;
  circleMinBudgetRenewal?: BudgetRenewalType;
  // circleIdentityId reuses an existing CircleIdentity — when set,
  // circleIdentityName/circlePolicy/providerPubkey below are ignored.
  circleIdentityId?: number;
  // circleIdentityName/circlePolicy/providerPubkey create a brand-new
  // CircleIdentity — used only when circleIdentityId is not set.
  circleIdentityName?: string;
  circlePolicy?: string;
  providerPubkey?: string;
  metadata?: AppMetadata;
  unlockPassword?: string; // required to create superuser apps
}

export interface CreateAppResponse {
  id: number;
  name: string;
  pairingUri: string;
  pairingPublicKey: string;
  pairingSecretKey: string;
  relayUrls: string[];
  walletPubkey: string;
  lud16: string;
  returnTo: string;
}

export type CircleDeleteMode = "all" | "empty_only";

export interface CircleChildBalance {
  appId: number;
  name: string;
  requesterPubkey: string;
  appPubkey: string;
  balanceMloki: number;
}

export interface ListCircleChildrenBalancesResponse {
  children: CircleChildBalance[];
  totalCount: number;
}

export interface DeleteCircleHubResult {
  hubDeleted: boolean;
  deletedChildIds: number[];
  skippedChildIds: number[];
}

export type UpdateAppRequest = {
  name?: string;
  maxAmount?: number;
  budgetRenewal?: string;
  expiresAt?: string | undefined;
  updateExpiresAt?: boolean;
  scopes?: Scope[];
  metadata?: AppMetadata;
  isolated?: boolean;
  // jit_hub only
  jitPerWalletMaxMloki?: number;
  jitMaxExpSecs?: number;
  // circle_hub only
  circleMaxExpSecs?: number;
  circleFeesPpm?: number;
  circlePerWalletMaxMloki?: number;
  circleMinBudgetRenewal?: BudgetRenewalType;
};

export type Channel = {
  localBalance: number;
  localSpendableBalance: number;
  remoteBalance: number;
  remotePubkey: string;
  id: string;
  fundingTxId: string;
  fundingTxVout: number;
  active: boolean;
  public: boolean;
  confirmations?: number;
  confirmationsRequired?: number;
  forwardingFeeBaseMloki: number;
  forwardingFeeProportionalMillionths: number;
  unspendablePunishmentReserve: number;
  counterpartyUnspendablePunishmentReserve: number;
  error?: string;
  status: "online" | "opening" | "offline";
  isOutbound: boolean;
};

export type UpdateChannelRequest = {
  forwardingFeeBaseMloki: number;
};

export type Peer = {
  nodeId: string;
  address: string;
  isPersisted: boolean;
  isConnected: boolean;
};

export type NodeConnectionInfo = {
  pubkey: string;
  address: string;
  port: number;
};

export type ConnectPeerRequest = {
  pubkey: string;
  address: string;
  port: number;
};

export type SignMessageRequest = {
  message: string;
};

export type SignMessageResponse = {
  message: string;
  signature: string;
};

export type PayInvoiceResponse = {
  preimage: string;
  fee: number;
};



export type CreateInvoiceRequest = {
  amount: number;
  description: string;
  lspJitChannelSCID?: string;
  lspCltvExpiryDelta?: number;
  lspPubkey?: string;
  lspFeeBaseMloki?: number;
  lspFeeProportionalMillionths?: number;
};

export type OpenChannelRequest = {
  pubkey: string;
  amountLoki: number;
  public: boolean;
};

export type OpenChannelResponse = {
  fundingTxId: string;
};

// eslint-disable-next-line @typescript-eslint/ban-types
export type CloseChannelResponse = {};

export type PendingBalancesDetails = {
  channelId: string;
  nodeId: string;
  amount: number;
  fundingTxId: string;
  fundingTxVout: number;
};

export type OnchainBalanceResponse = {
  spendable: number;
  total: number;
  reserved: number;
  pendingBalancesFromChannelClosures: number;
  pendingBalancesDetails: PendingBalancesDetails[];
  pendingSweepBalancesDetails: PendingBalancesDetails[];
};

export type MempoolUtxo = {
  txid: string;
  vout: number;
  status: {
    confirmed: boolean;
    block_height?: number;
    block_hash?: string;
    block_time?: number;
  };
  value: number;
};

export type MempoolNode = {
  alias: string;
  public_key: string;
  color: string;
  active_channel_count: number;
  sockets: string;
};

export type MempoolTransaction = {
  txid: string;
  //version: 1,
  //locktime: 0,
  // vin: [],
  //vout: [],
  size: number;
  weight: number;
  fee: number;
  status:
    | {
        confirmed: true;
        block_height: number;
        block_hash: string;
        block_time: number;
      }
    | { confirmed: false };
};

export type LongUnconfirmedZeroConfChannel = { id: string; message: string };

export type SetupNodeInfo = Partial<{
  backendType: BackendType;

  mnemonic?: string;
  nextBackupReminder?: string;

  flndAddress?: string;
  flndCertHex?: string;
  flndMacaroonHex?: string;

  autoConnect?: boolean;
  // customConfig removed
  
  lokihubServicesURL?: string;
  swapServiceUrl?: string;
  relay?: string;
  messageboardNwcUrl?: string;
  mempoolApi?: string;
  enableSwap?: boolean;
  enableMessageboardNwc?: boolean;
  lsps?: LSP[];
}>;

export type LSPType = "LSPS1";



export type LokiInfo = {
  version: string;
  releaseNotes: string; // Markdown format
};

export type FlokicoinRate = {
  code: string;
  symbol: string;
  rate: string;
  rate_float: number;
};

// TODO: use camel case (needs mapping in the Loki OAuth Service - see how LokiInfo is done above)
export type LokiMe = {
  identifier: string;
  nostr_pubkey: string;
  lightning_address: string;
  email: string;
  name: string;
  avatar: string;
  keysend_pubkey: string;
  shared_node: boolean;
  hub: {
    name?: string;
    config?: {
      region?: string;
    };
  };
  subscription: {
    plan_code: string;
  };
};

export type LSPOrderRequest = {
  amount: number;
  lspType: LSPType;
  lspIdentifier: string;
  public: boolean;
};

export type LSPOrderResponse = {
  invoice?: string;
  fee: number;
  invoiceAmount: number;
  incomingLiquidity: number;
  outgoingLiquidity: number;
};

export type AutoChannelRequest = {
  isPublic: boolean;
};
export type AutoChannelResponse = {
  invoice?: string;
  fee?: number;
  channelSize: number;
};

export type RedeemOnchainFundsResponse = {
  txId: string;
};

export type LightningBalanceResponse = {
  totalSpendable: number;
  totalReceivable: number;
  nextMaxSpendable: number;
  nextMaxReceivable: number;
  nextMaxSpendableMPP: number;
  nextMaxReceivableMPP: number;
};

export type BalancesResponse = {
  onchain: OnchainBalanceResponse;
  lightning: LightningBalanceResponse;
};

export type Transaction = {
  type: "incoming" | "outgoing";
  state: "settled" | "pending" | "failed";
  appId: number | undefined;
  invoice: string;
  description: string;
  descriptionHash: string;
  preimage: string | undefined;
  paymentHash: string;
  amount: number;
  feesPaid: number;
  updatedAt: string;
  createdAt: string;
  settledAt: string | undefined;
  metadata?: TransactionMetadata;
  boostagram?: Boostagram;
  failureReason: string;
};

export type TransactionMetadata = {
  comment?: string; // LUD-12
  payer_data?: {
    email?: string;
    name?: string;
    pubkey?: string;
  }; // LUD-18
  recipient_data?: {
    identifier?: string;
  }; // LUD-18
  nostr?: {
    pubkey: string;
    tags: string[][];
  }; // NIP-57

  swap_id?: string;
} & Record<string, unknown>;

export type Boostagram = {
  appName: string;
  name: string;
  podcast: string;
  url: string;
  episode?: string;
  feedId?: string;
  itemId?: string;
  ts?: number;
  message?: string;
  senderId: string;
  senderName: string;
  time: string;
  action: "boost";
  valueMlokiTotal: number;
};

export type OnchainTransaction = {
  amountLoki: number;
  createdAt: number;
  type: "incoming" | "outgoing";
  state: "confirmed" | "unconfirmed";
  numConfirmations: number;
  txId: string;
};

export type LSPS1GetInfoResponse = LSPS1Option;

export type LSPS1Option = {
  min_required_channel_confirmations: number;
  min_initial_client_balance_loki: number;
  max_initial_client_balance_loki: number;
  min_initial_lsp_balance_loki: number;
  max_initial_lsp_balance_loki: number;
  min_channel_balance_loki: number;
  max_channel_balance_loki: number;
  opening_fee_params: LSPS1OpeningFeeParams[];
};

export interface LSPS1OpeningFeeParams {
  min_fee_mloki: string;
  proportional: number;
  valid_until: string;
  min_lifetime: number;
  max_client_to_self_delay: number;
  min_payment_size_mloki: string;
  max_payment_size_mloki: string;
  promise: string;
}

export type LSPS1CreateOrderRequest = {
  lsp_pubkey: string;
  amount_loki: number;
  channel_expiry_blocks: number;
  token?: string;
  refund_onchain_address?: string;
  announce_channel?: boolean;
  opening_fee_params?: LSPS1OpeningFeeParams;
};

export type LSPS1CreateOrderResponse = {
  order_id: string;
  payment_invoice: string;
  fee_total_loki?: number;
  order_total_loki?: number;
};

export type LSPS1GetOrderResponse = {
    order_id: string;
    state: string;
    payment_invoice: string;
    fee_total_loki?: number;
    order_total_loki?: number;
};

export interface LSPS1Order {
  orderId: string;
  lspPubkey: string;
  state: string;
  paymentInvoice: string;
  feeTotal: number;
  orderTotal: number;
  lspBalanceLoki?: number;
  clientBalanceLoki: number;
  createdAt: string;
  updatedAt: string;
}

export type LSPS1ListOrdersResponse = {
  orders: LSPS1Order[];
};


export type ListAppsResponse = {
  apps: App[];
  totalCount: number;
};

export type ListTransactionsResponse = {
  transactions: Transaction[];
  totalCount: number;
};

export type NewChannelOrderStatus = "pay" | "paid" | "success" | "opening";

type NewChannelOrderCommon = {
  amount: string;
  isPublic: boolean;
  status: NewChannelOrderStatus;
  fundingTxId?: string;
  prevChannelIds: string[];
};

export type OnchainOrder = {
  paymentMethod: "onchain";
  pubkey: string;
  host: string;
} & NewChannelOrderCommon;

export type LightningOrder = {
  paymentMethod: "lightning";
  lspType: LSPType;
  lspIdentifier: string;
} & NewChannelOrderCommon;

export type NewChannelOrder = OnchainOrder | LightningOrder;

export type AuthTokenResponse = {
  token: string;
};


export type GetForwardsResponse = {
  outboundAmountForwardedMloki: number;
  totalFeeEarnedMloki: number;
  numForwards: number;
};

export interface FAQ {
  question: string;
  answer: string;
}

export interface LSPS2OpeningFeeParams {
  min_fee_mloki: string;
  proportional: number;
  valid_until: string;
  min_lifetime: number;
  max_client_to_self_delay: number;
  min_payment_size_mloki: string;
  max_payment_size_mloki: string;
  promise: string;
}

export type LSPS2GetInfoResponse = LSPS2OpeningFeeParams;

export interface LSPS2BuyRequest {
  lspPubkey: string;
  paymentSizeMloki?: number;
  openingFeeParams?: LSPS2OpeningFeeParams;
}

export interface LSPS2BuyResponse {
  requestId: string;
  interceptScid: string; // Backend returns as string
  cltvExpiryDelta: number;
  lspNodeID: string;
}

// JITWalletClaim represents one recipient's slice of a (possibly shared)
// jit_wallet. id is the claim's own row ID (DELETE
// /jit-wallets/{wallet_app_id}/claims/{id}, unclaimed only); wallet_app_id
// identifies the shared connection this slice belongs to (reveal via
// /jit-connection, delete the whole wallet via DELETE
// /jit-wallets/{wallet_app_id}). claimed is a plain boolean — claim_funds
// either pays a slice out completely or not at all, so there's no
// partial/active state to derive.
export interface JITWalletClaim {
  id: number;
  wallet_app_id: number;
  identity_type: "pubkey" | "connection_key";
  identity_value: string;
  amount_mloki: number;
  expires_at?: number;
  claimed: boolean;
  claimed_at?: number;
  created_at: number;
}

export type JITAllocationStatus = "unclaimed" | "claimed" | "expired";

export interface JITWalletClaimCounts {
  all: number;
  unclaimed: number;
  claimed: number;
  expired: number;
}

export interface ListJITWalletClaimsResponse {
  claims: JITWalletClaim[];
  totalCount: number;
  counts: JITWalletClaimCounts;
}

// JITWalletRecipient describes one recipient's requested slice when creating
// a (possibly shared) JIT wallet.
export interface JITWalletRecipient {
  identity_type: "pubkey" | "connection_key";
  identity_value: string;
  ia_pubkey?: string; // required iff identity_type === "connection_key"
  amount_mloki: number;
}

export interface CreateJITWalletResponse {
  app_id: number;
  pairing_uri: string;
  expires_at: number;
  recipients: JITWalletRecipient[];
}

export interface IdentityAuthority {
  pubkey: string;
  name: string;
  relay_urls?: string[];
  created_at: number;
}

