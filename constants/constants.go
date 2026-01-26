package constants

import "time"

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

const SUBWALLET_APPSTORE_APP_ID = "uncle-jim" // Thanks jim

const (
	FLOKICOIN_DISPLAY_FORMAT_BIP177 = "bip177"
	FLOKICOIN_DISPLAY_FORMAT_LOKI   = "loki"
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
	LSPS5_EVENT_NOTIFICATION                = "lsps5.notification"
	LSPS5_EVENT_PAYMENT_INCOMING            = "lsps5.payment_incoming"
	LSPS5_EVENT_EXPIRY_SOON                 = "lsps5.expiry_soon"
	LSPS5_EVENT_LIQUIDITY_REQUEST           = "lsps5.liquidity_request"
	LSPS5_EVENT_ONION_MESSAGE               = "lsps5.onion_message"
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
