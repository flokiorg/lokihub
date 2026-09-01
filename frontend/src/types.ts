import {
  ArrowRightLeftIcon,
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
  | "cash_hub"
  | "circle_wallet"
  | "cash_redeem"
  | "cash_transfer";

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
  cash_hub: NetworkIcon,
  circle_wallet: UsersIcon,
  cash_redeem: GiftIcon,
  cash_transfer: ArrowRightLeftIcon,
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
  cash_hub: "Mint spend-only Lokicash on demand to beneficiaries",
  circle_wallet: "Issue wallets to your circle's members",
  cash_redeem: "Claim your allocated share of a shared Cash wallet",
  cash_transfer: "Transfer or split an unclaimed share to someone else",
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
  // parentAppId/parentKind are set only on a Cash/Circle Hub's own children
  // (cash_wallet/circle_wallet), identifying which specific hub issued
  // them — used to group siblings under the same hub in the connection
  // switcher, instead of the hub's own id/kind.
  parentAppId?: number;
  parentKind?: string;
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
  // cashPerWalletMaxMloki/cashMaxExpSecs/cashMinTransferMloki/cashRedeemFeePpm
  // are set only for cash_hub apps — the hub-wide defaults set at creation
  // time, editable from Edit Connection. cashMinTransferMloki (0 = no floor)
  // is the default floor a freshly-minted wallet's slices inherit for
  // cash_transfer splitting. cashRedeemFeePpm (0 = free) is the default
  // per-million cash_redeem fee a freshly-minted wallet's slices inherit,
  // charged only on a genuine external redemption.
  cashPerWalletMaxMloki?: number;
  cashMaxExpSecs?: number;
  cashMinTransferMloki?: number;
  cashRedeemFeePpm?: number;
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
  // cashHub/cashPerWalletMaxLoki/cashMaxExpSecs are only meaningful for a new
  // connection (isNewConnection) — they select kind "cash_hub" instead of
  // "isolated" at creation time and are never editable afterward here (see
  // AppDetails' dedicated Hub Settings card for that).
  cashHub?: boolean;
  cashPerWalletMaxLoki?: number;
  cashMaxExpSecs?: number;
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
  // Full nostr pubkey behind a Cash/circle wallet's identity, stored for
  // display purposes (resolving a profile name instead of the npub prefix
  // baked into the app name). "identity_pubkey" for Cash wallets (pubkey mode
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
  cashPerWalletMaxMloki?: number;
  cashMaxExpSecs?: number;
  cashMinTransferMloki?: number;
  cashRedeemFeePpm?: number;
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
  // cash_hub only
  cashPerWalletMaxMloki?: number;
  cashMaxExpSecs?: number;
  cashMinTransferMloki?: number;
  cashRedeemFeePpm?: number;
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

// CashWalletClaim represents one recipient's slice of a (possibly shared)
// cash_wallet. id is the claim's own row ID (DELETE
// /cash-wallets/{wallet_app_id}/claims/{id}, unclaimed only); wallet_app_id
// identifies the shared connection this slice belongs to (reveal via
// /cash-connection, delete the whole wallet via DELETE
// /cash-wallets/{wallet_app_id}). claimed is a plain boolean — cash_redeem
// either pays a slice out completely or not at all, so there's no
// partial/active state to derive.
export interface CashWalletClaim {
  id: number;
  wallet_app_id: number;
  // "bearer" has no identity at all — identity_value is a one-way
  // commitment (sha256 of a secret only the recipient holds), never an
  // actual identity, and there is always exactly one claim row per wallet
  // when this is "bearer" (NIP-CASH §Bearer Slices: a bearer slice never
  // shares a wallet with another recipient).
  identity_type: "pubkey" | "connection_key" | "bearer";
  identity_value: string;
  amount_mloki: number;
  expires_at?: number;
  claimed: boolean;
  claimed_at?: number;
  created_at: number;
  // min_transfer_mloki floors how small a future split off this slice (or
  // its remainder) may be (0 = no floor). Set once, at creation or inherited
  // from a split's source slice.
  min_transfer_mloki: number;
  // redeem_fee_ppm is this slice's own locked-in redeem fee rate (0 = free),
  // fixed once at creation or inherited unchanged from a split's source
  // slice — never retroactively affected by a later change to the Hub's own
  // default.
  redeem_fee_ppm: number;
  // Set when this slice's value was moved into a brand-new dedicated
  // cash_wallet via a split, rather than redeemed — purely informational.
  spun_off_to_wallet_app_id?: number;
  // The wallet's own connection, packaged as a lokicash1... string —
  // identical for every claim sharing the same wallet_app_id. Only
  // populated for the page of results actually returned.
  cash_token?: string;
}

export type CashAllocationStatus = "unclaimed" | "claimed" | "expired";

export interface CashWalletClaimCounts {
  all: number;
  unclaimed: number;
  claimed: number;
  expired: number;
}

export interface ListCashWalletClaimsResponse {
  claims: CashWalletClaim[];
  totalCount: number;
  counts: CashWalletClaimCounts;
}

// CashWalletRecipient describes one recipient's requested slice when
// creating a (possibly shared) Cash wallet. For identity_type === "bearer",
// the caller MUST NOT set identity_value or ia_pubkey — the wallet mints the
// bearer secret itself, and a bearer recipient MUST be the request's only
// one (NIP-CASH §Bearer Slices).
export interface CashWalletRecipient {
  identity_type: "pubkey" | "connection_key" | "bearer";
  identity_value?: string;
  ia_pubkey?: string; // required iff identity_type === "connection_key"
  amount_mloki: number;
  // bearer_secret is response-only: populated when identity_type ===
  // "bearer", and only in the create_cash_wallet response — it is never
  // retrievable again afterward (NIP-CASH §Bearer Slices).
  bearer_secret?: string;
}

export interface CreateCashWalletResponse {
  app_id: number;
  pairing_uri: string;
  cash_token: string;
  expires_at: number;
  recipients: CashWalletRecipient[];
}

// CashWalletConnectionResponse is GET /api/apps/{id}/cash-connection's
// response — the same connection data CreateCashWalletResponse carries,
// re-derivable at any later time (NIP-CASH §The Pairing Connection).
export interface CashWalletConnectionResponse {
  pairing_uri: string;
  cash_token: string;
}

export interface IdentityAuthority {
  pubkey: string;
  name: string;
  relay_urls?: string[];
  created_at: number;
  // How many currently-unclaimed connection_key Cash Wallet slices this IA
  // has attested for right now — the live blast radius a revocation would
  // immediately strand. Populated by GET /api/identity-authorities; not
  // meaningful on a just-added IA's own POST response (nothing to count yet).
  unredeemed_slice_count: number;
}
