package constants

import (
	"errors"
	"time"
)

// Sentinel errors used by service and controller layers.
var (
	ErrInvalidParams     = errors.New("invalid params")
	ErrKindImmutable     = errors.New("app kind is immutable")
	ErrQuotaExceeded     = errors.New("quota exceeded")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrDuplicate         = errors.New("duplicate entry")

	// ErrSocialCacheWarmingUp is returned by a following-policy authorization
	// check when the hub's in-memory Nostr social cache hasn't finished its
	// initial startup warm-up yet (see service.StartNostrSocialCacheRefresher).
	// Lives here rather than in service so nip47/controllers can check it
	// without importing service (see the service→nip47→nip47/controllers cycle
	// note on controllers.NostrSocialCache).
	ErrSocialCacheWarmingUp = errors.New("social cache is still warming up")
)

// shared constants used by multiple packages

const (
	TRANSACTION_TYPE_INCOMING = "incoming"
	TRANSACTION_TYPE_OUTGOING = "outgoing"

	TRANSACTION_STATE_PENDING  = "PENDING"
	TRANSACTION_STATE_SETTLED  = "SETTLED"
	TRANSACTION_STATE_FAILED   = "FAILED"
	TRANSACTION_STATE_ACCEPTED = "ACCEPTED"

	SWAP_TYPE_IN  = "in"
	SWAP_TYPE_OUT = "out"

	SWAP_STATE_PENDING  = "PENDING"
	SWAP_STATE_SUCCESS  = "SUCCESS"
	SWAP_STATE_FAILED   = "FAILED"
	SWAP_STATE_REFUNDED = "REFUNDED"
)

const (
	APP_SHUTDOWN_TIMEOUT = 15 * time.Second
)

const (
	BUDGET_RENEWAL_DAILY   = "daily"
	BUDGET_RENEWAL_WEEKLY  = "weekly"
	BUDGET_RENEWAL_MONTHLY = "monthly"
	BUDGET_RENEWAL_YEARLY  = "yearly"
	BUDGET_RENEWAL_NEVER   = "never"
)

func GetBudgetRenewals() []string {
	return []string{
		BUDGET_RENEWAL_DAILY,
		BUDGET_RENEWAL_WEEKLY,
		BUDGET_RENEWAL_MONTHLY,
		BUDGET_RENEWAL_YEARLY,
		BUDGET_RENEWAL_NEVER,
	}
}

// budgetRenewalRank orders renewal periods from tightest (daily) to loosest
// (never, i.e. the spend cap is never reset — the closest thing to "no
// periodic budget" the enum has).
var budgetRenewalRank = map[string]int{
	BUDGET_RENEWAL_DAILY:   0,
	BUDGET_RENEWAL_WEEKLY:  1,
	BUDGET_RENEWAL_MONTHLY: 2,
	BUDGET_RENEWAL_YEARLY:  3,
	BUDGET_RENEWAL_NEVER:   4,
}

// BudgetRenewalRank returns renewal's position in the tightest-to-loosest
// ordering above, or -1 if renewal isn't one of GetBudgetRenewals(). Callers
// use this to enforce renewal-frequency bounds with plain integer comparison,
// e.g. a Circle Hub's min_budget_renewal floor (reject requests with a
// lower/tighter rank than the floor).
func BudgetRenewalRank(renewal string) int {
	rank, ok := budgetRenewalRank[renewal]
	if !ok {
		return -1
	}
	return rank
}

const (
	PAY_INVOICE_SCOPE       = "pay_invoice" // also covers pay_keysend and multi_* payment methods
	GET_BALANCE_SCOPE       = "get_balance"
	GET_INFO_SCOPE          = "get_info"
	MAKE_INVOICE_SCOPE      = "make_invoice"
	LOOKUP_INVOICE_SCOPE    = "lookup_invoice"
	LIST_TRANSACTIONS_SCOPE = "list_transactions"
	SIGN_MESSAGE_SCOPE      = "sign_message"
	NOTIFICATIONS_SCOPE     = "notifications" // covers all notification types
	SUPERUSER_SCOPE         = "superuser"

	// JIT Hub scope — grants create_jit_wallet on an isolated wallet
	JIT_HUB_SCOPE = "jit_hub"
	// Circle Wallet scope — grants create_circle_wallet on a circle_admin wallet
	CIRCLE_WALLET_SCOPE = "circle_wallet"
	// JIT Claim Funds scope — granted on jit_wallet children only. Covers
	// claim_funds (pay out a recipient's proven slice) and list_recipients
	// (read-only roster of a shared wallet's recipients/claim status).
	// Deliberately does NOT cover pay_invoice/lookup_invoice/list_transactions:
	// a jit_wallet's connection may be widely shared, so its method surface is
	// a narrow, explicit allowlist rather than a normal wallet's scope set.
	JIT_CLAIM_FUNDS_SCOPE = "jit_claim_funds"
)

// NIP-47 method names for JIT and Circle Wallet operations.
const (
	NIP47MethodCreateJITWallet    = "create_jit_wallet"
	NIP47MethodCreateCircleWallet = "create_circle_wallet"
	// NIP47MethodClaimFunds pays out a proven recipient's slice of a shared
	// jit_wallet in one shot. Replaces the old, per-recipient
	// create_jit_wallet/claim_jit_wallet reveal flow entirely.
	NIP47MethodClaimFunds = "claim_funds"
	// NIP47MethodListRecipients is a read-only roster of a shared jit_wallet's
	// recipients (identity, entitled amount, claimed status) — no invoice or
	// preimage detail, since a jit_wallet has no list_transactions grant.
	NIP47MethodListRecipients = "list_recipients"
)

// PayCapableScopes lists every scope whose AppPermission row can carry
// MaxAmountLoki/BudgetRenewal budget semantics for SendPaymentSync/
// SendKeysend and for app budget/expiry display (api.GetApp/ListApps/
// UpdateApp, get_budget). Historically only PAY_INVOICE_SCOPE played this
// role; JIT_CLAIM_FUNDS_SCOPE joins it because jit_wallet children no longer
// carry PAY_INVOICE_SCOPE at all. Any future scope that authorizes its own
// distinct payment-shaped method should be added here too.
var PayCapableScopes = []string{PAY_INVOICE_SCOPE, JIT_CLAIM_FUNDS_SCOPE}

// PPM_DIVISOR is the parts-per-million base used by CircleHubConfig.FeesPpm:
// a circle_hub forwarding fee of feesPpm on an outgoing payment of amount
// mloki skims floor(amount * feesPpm / PPM_DIVISOR) mloki. 1_000_000 ppm is
// therefore 100% — MAX_FEES_PPM below caps configured values at that ceiling.
const PPM_DIVISOR = 1_000_000

// MAX_FEES_PPM is the upper bound accepted for CircleHubConfig.FeesPpm
// (1_000_000 ppm = 100%, skimming the entire payment makes no sense but is
// the mathematically sound ceiling; UpdateCircleHubConfig/CreateCircleHub
// reject anything higher).
const MAX_FEES_PPM = PPM_DIVISOR

// DefaultGeneralRelays seeds the "GeneralRelay" config key on first run.
// These relays are used to fetch general Nostr social data — profiles,
// notes, and events, including Circle contact lists (kind:0/kind:1/kind:3) —
// and are independent of the NWC "Relay" config.
var DefaultGeneralRelays = []string{
	"wss://relay.damus.io",
	"wss://relay.primal.net",
	"wss://nos.lol",
	"wss://relay.ohstr.com",
}

// DefaultSearchRelays seeds the "SearchRelay" config key on first run. These
// relays are used only for NIP-50 search queries and are independent of both
// the NWC "Relay" config and "GeneralRelay".
var DefaultSearchRelays = []string{
	"wss://relay.ohstr.com",
}

// Additional NIP-47 error codes used by new controllers.
const (
	ERROR_RATE_LIMITED  = "RATE_LIMITED"
	ERROR_NOT_SUPPORTED = "NOT_SUPPORTED"
	ERROR_NOT_READY     = "NOT_READY"
)

// limit encoded metadata length, otherwise relays may have trouble listing multiple transactions
// given a relay limit of 512000 bytes and ideally being able to list 25 transactions,
// each transaction would have to have a maximum size of 20480
// accounting for encryption and other metadata in the response, this is set to 4096 characters
const INVOICE_METADATA_MAX_LENGTH = 4096

// errors used by NIP-47 and the transaction service
const (
	ERROR_INTERNAL               = "INTERNAL"
	ERROR_NOT_IMPLEMENTED        = "NOT_IMPLEMENTED"
	ERROR_QUOTA_EXCEEDED         = "QUOTA_EXCEEDED"
	ERROR_INSUFFICIENT_BALANCE   = "INSUFFICIENT_BALANCE"
	ERROR_UNAUTHORIZED           = "UNAUTHORIZED"
	ERROR_EXPIRED                = "EXPIRED"
	ERROR_RESTRICTED             = "RESTRICTED"
	ERROR_BAD_REQUEST            = "BAD_REQUEST"
	ERROR_NOT_FOUND              = "NOT_FOUND"
	ERROR_UNSUPPORTED_ENCRYPTION = "UNSUPPORTED_ENCRYPTION"
	ERROR_OTHER                  = "OTHER"
)

const (
	ENCRYPTION_TYPE_NIP04    = "nip04"
	ENCRYPTION_TYPE_NIP44_V2 = "nip44_v2"
)

const SUBWALLET_APPSTORE_APP_ID = "lokies"

const (
	FLOKICOIN_DISPLAY_FORMAT_FLC  = "flc"
	FLOKICOIN_DISPLAY_FORMAT_LOKI = "loki"
	FLOKICOIN_DISPLAY_FORMAT_AUTO = "auto"
)

const (
	APP_STORE_SYNC_INTERVAL = 6 * time.Hour
	APP_STORE_CACHE_DIR     = "appstore"
)

const (
	DEFAULT_ENABLE_NOSTR_NOTIFICATIONS = true
	DEFAULT_ENABLE_HTTP_WEBHOOKS       = false
	DEFAULT_ENABLE_POLLING             = false
	APP_IDENTIFIER                     = "lokihub"
)

// LSPS5 Internal Event Names
const (
	LSPS5_EVENT_NOTIFICATION      = "lsps5.notification"
	LSPS5_EVENT_PAYMENT_INCOMING  = "lsps5.payment_incoming"
	LSPS5_EVENT_EXPIRY_SOON       = "lsps5.expiry_soon"
	LSPS5_EVENT_LIQUIDITY_REQUEST = "lsps5.liquidity_request"
	LSPS5_EVENT_ONION_MESSAGE     = "lsps5.onion_message"
	// LSPS5_EVENT_ORDER_STATE_NOTIFICATION is the raw inbound notification (Nostr/webhook received,
	// DB not yet updated). Only lspsEventConsumer reacts to it.
	LSPS5_EVENT_ORDER_STATE_NOTIFICATION = "lsps5.order_state_notification"
	// LSPS5_EVENT_ORDER_STATE_CHANGED is published by HandleOrderStateUpdate after the DB write.
	// The frontend SSE subscriber forwards this to the browser.
	LSPS5_EVENT_ORDER_STATE_CHANGED         = "lsps5.order_state_changed"
	LSPS5_EVENT_WEBHOOK_REGISTERED          = "lsps5.webhook_registered"
	LSPS5_EVENT_WEBHOOK_REGISTRATION_FAILED = "lsps5.webhook_registration_failed"
	LSPS5_EVENT_WEBHOOKS_LISTED             = "lsps5.webhooks_listed"
	LSPS5_EVENT_WEBHOOK_REMOVED             = "lsps5.webhook_removed"
	LSPS5_EVENT_WEBHOOK_REMOVAL_FAILED      = "lsps5.webhook_removal_failed"
)

// LSPS1 Internal Event Names
const (
	LSPS1_EVENT_NOTIFICATION = "lsps1.notification"
)
