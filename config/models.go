package config

import "github.com/ohstr/nmilat/nipcash/transport"

const (
	FLNDBackendType = "FLND"
)

const (
	OnchainAddressKey           = "OnchainAddress"
	AutoSwapBalanceThresholdKey = "AutoSwapBalanceThreshold"
	AutoSwapAmountKey           = "AutoSwapAmount"
	AutoSwapDestinationKey      = "AutoSwapDestination"
	AutoSwapXpubIndexStart      = "AutoSwapXpubIndexStart"
)

type AppConfig struct {
	Relay         string `envconfig:"RELAY"`
	LNBackendType string `envconfig:"LN_BACKEND_TYPE"`

	// FLND (Flokicoin) Backend
	FLNDAddress      string `envconfig:"FLND_ADDRESS"`
	FLNDCertFile     string `envconfig:"FLND_CERT_FILE"`
	FLNDMacaroonFile string `envconfig:"FLND_MACAROON_FILE"`

	Workdir     string `envconfig:"WORK_DIR"`
	Port        string `envconfig:"PORT" default:"1610"`
	DatabaseUri string `envconfig:"DATABASE_URI" default:"nwc.db"`
	LogLevel    string `envconfig:"LOG_LEVEL" default:"4"`
	LogToFile   bool   `envconfig:"LOG_TO_FILE" default:"true"`
	Network     string `envconfig:"NETWORK"`
	MempoolApi  string `envconfig:"MEMPOOL_API"`
	BaseUrl     string `envconfig:"BASE_URL"`
	FrontendUrl string `envconfig:"FRONTEND_URL"`

	GoProfilerAddr      string `envconfig:"GO_PROFILER_ADDR"`
	EnableAdvancedSetup bool   `envconfig:"ENABLE_ADVANCED_SETUP" default:"true"`
	AutoUnlockPassword  string `envconfig:"AUTO_UNLOCK_PASSWORD"`
	LogDBQueries        bool   `envconfig:"LOG_DB_QUERIES" default:"false"`
	SwapServiceUrl      string `envconfig:"SWAP_SERVICE_URL"`
	LokihubServicesURL  string `envconfig:"LOKIHUB_SERVICES_URL" default:"https://raw.githubusercontent.com/flokiorg/lokihub-services/refs/heads/main"`
	LokihubStoreURL     string `envconfig:"LOKIHUB_STORE_URL" default:"https://raw.githubusercontent.com/flokiorg/lokihub-store/refs/heads/main"`
	MessageboardNwcUrl  string `envconfig:"MESSAGEBOARD_NWC_URL"`

	EnableSwap            bool   `envconfig:"ENABLE_SWAP" default:"false"`
	EnableMessageboardNwc bool   `envconfig:"ENABLE_MESSAGEBOARD_NWC" default:"false"`
	LSP                   string `envconfig:"LSP"`

	// CashWalletRateLimitPerHour caps mint_cash calls per calling app
	// pubkey. 0 disables the limit entirely (useful for dev/integration testing
	// against a shared long-lived hub).
	CashWalletRateLimitPerHour int `envconfig:"CASH_WALLET_RATE_LIMIT_PER_HOUR" default:"10"`
	// CashClaimCallsPerHour caps the cash_redeem, cash_transfer and
	// cash_consolidate calls one verified claim signer, or one cash-mode
	// commitment, may make per hour; the three share it. Never keyed by a bill.
	// 0 disables the limit.
	CashClaimCallsPerHour int `envconfig:"CASH_CLAIM_CALLS_PER_HOUR" default:"120"`
	// CashRedeemMaxInvoiceExpirySecs and CashRedeemMaxFinalCLTV are the redeem
	// invoice policy: an invoice whose expiry or final CLTV delta exceeds them is
	// refused RESTRICTED before the claim. 0 disables the check. The CLTV limit
	// is in blocks: 2880 is two days of one-minute Flokicoin blocks, above flnd's
	// own invoice default (800).
	CashRedeemMaxInvoiceExpirySecs int `envconfig:"CASH_REDEEM_MAX_INVOICE_EXPIRY_SECS" default:"86400"`
	CashRedeemMaxFinalCLTV         int `envconfig:"CASH_REDEEM_MAX_FINAL_CLTV" default:"2880"`
	// CashRedeemInflightPerHub and CashRedeemInflightNode bound the cash_redeem
	// payouts in flight per Cash Hub and node-wide; over either a redeem is
	// RATE_LIMITED before it claims anything. 0 disables that bound.
	CashRedeemInflightPerHub int `envconfig:"CASH_REDEEM_INFLIGHT_PER_HUB" default:"32"`
	CashRedeemInflightNode   int `envconfig:"CASH_REDEEM_INFLIGHT_NODE" default:"256"`
	// CashRedeemLateFailureCooldownSecs is how long a slice whose payout failed
	// only after cash_redeem answered is refused RATE_LIMITED for another
	// redeem. 0 disables the cool-down.
	CashRedeemLateFailureCooldownSecs int `envconfig:"CASH_REDEEM_LATE_FAILURE_COOLDOWN_SECS" default:"600"`
	// CashRedeemRouteMarginPpm and CashRedeemRouteMarginMinMillis are the margin
	// over the quoted redeem fee that an external redeem may pay for its route:
	// fee + max(ppm × amount / 1e6, min).
	CashRedeemRouteMarginPpm       int `envconfig:"CASH_REDEEM_ROUTE_MARGIN_PPM" default:"2000"`
	CashRedeemRouteMarginMinMillis int `envconfig:"CASH_REDEEM_ROUTE_MARGIN_MIN_MILLIS" default:"1000"`
	// CashRedeemReplyWaitSecs is how long cash_redeem waits for its payment
	// before answering INTERNAL with the slice kept claimed. 0 means 20.
	CashRedeemReplyWaitSecs int `envconfig:"CASH_REDEEM_REPLY_WAIT_SECS" default:"20"`
	// CashLineageSpinOffsPerHour bounds how often one bill's lineage spins off
	// new bills, per hour (NIP-CASH §Security Considerations for Cash-Mode
	// Slices); over it a spin-off is RATE_LIMITED. 0 disables the limit.
	CashLineageSpinOffsPerHour int `envconfig:"CASH_LINEAGE_SPIN_OFFS_PER_HOUR" default:"20"`
	// CashMaxLiveBillsPerHub caps the live bills under one Cash Hub that a
	// spin-off or consolidate may create; over it the call is QUOTA_EXCEEDED. A
	// mint is the hub owner's own call and is not bounded. 0 disables the cap.
	CashMaxLiveBillsPerHub int `envconfig:"CASH_MAX_LIVE_BILLS_PER_HUB" default:"10000"`
	// CircleWalletRateLimitPerHour caps create_circle_wallet calls per calling
	// app pubkey. 0 disables the limit entirely.
	CircleWalletRateLimitPerHour int `envconfig:"CIRCLE_WALLET_RATE_LIMIT_PER_HOUR" default:"3"`
	// CashSplitLinkRetentionDays is how long an archived cash slice keeps the
	// link to the bill its value moved into (a split or a consolidation). A
	// consolidation's links say which bills shared a holder, and NIP-CASH says a
	// Hub SHOULD NOT keep that longer than its audit policy needs. Amounts and
	// outcomes stay. 0 keeps the links forever.
	CashSplitLinkRetentionDays int `envconfig:"CASH_SPLIT_LINK_RETENTION_DAYS" default:"90"`

	// The four knobs below bound one private-transport batch envelope. They are
	// node-level rather than per-Cash-Hub on purpose: the transport gate runs
	// before any item has been attributed to a Hub, and a single envelope can
	// carry items targeting bills from different Hubs, so there is no per-Hub
	// policy to consult at the point where the limit has to be enforced.
	//
	// Each is a default only — a runtime value set through hub settings wins.
	// See config.PrivateEnvelopeLimits. Defaults mirror
	// nipcash/transport.DefaultLimits so the two cannot drift; 0 means "use the
	// SDK default".

	// PrivateEnvelopeMaxBytes caps the PADDED plaintext of one envelope. Bounded
	// by NIP-44's 65535-byte plaintext ceiling. Raising it also raises what the
	// relay must accept, since the base64 ciphertext is ~4/3 of this.
	PrivateEnvelopeMaxBytes int `envconfig:"PRIVATE_ENVELOPE_MAX_BYTES" default:"0"`
	// PrivateEnvelopeMaxItems is a cheap pre-check on item count, applied before
	// anything is parsed. Bytes are the real constraint.
	PrivateEnvelopeMaxItems int `envconfig:"PRIVATE_ENVELOPE_MAX_ITEMS" default:"0"`
	// PrivateEnvelopePadBucketBytes is the padding granularity that makes a
	// one-item envelope indistinguishable in size from a small batch.
	PrivateEnvelopePadBucketBytes int `envconfig:"PRIVATE_ENVELOPE_PAD_BUCKET_BYTES" default:"0"`
	// PrivateEnvelopeMaxVerifyBudget caps the signature verifications one
	// envelope may demand, counted structurally before any crypto runs.
	PrivateEnvelopeMaxVerifyBudget int `envconfig:"PRIVATE_ENVELOPE_MAX_VERIFY_BUDGET" default:"0"`
	// PrivateEnvelopeMaxConsolidateSources caps sources in one cash_consolidate
	// item on the private transport, and is necessarily LOWER than NIP-CASH's own
	// cap of 100 on the standard path: each source carries its own signed proof
	// (~952 bytes), so 100 sources is a ~96 KB item, past NIP-44's 65535-byte
	// ceiling and therefore impossible to encrypt at any envelope size.
	PrivateEnvelopeMaxConsolidateSources int `envconfig:"PRIVATE_ENVELOPE_MAX_CONSOLIDATE_SOURCES" default:"0"`
	// PrivateInboxEventsPerMin is how many envelopes a minute the hub decrypts
	// from one relay connection; the rest are dropped undecrypted. 0 disables
	// the limit.
	PrivateInboxEventsPerMin int `envconfig:"PRIVATE_INBOX_EVENTS_PER_MIN" default:"600"`
	// CashMaxRequestAgeSecs is the horizon H (NIP-CASH §Deadlines): how far past
	// arrival a request's deadline may lie, and the record-retention unit. 0 means
	// the default (300); accepted 60 to 86400.
	CashMaxRequestAgeSecs int `envconfig:"CASH_MAX_REQUEST_AGE_SECS" default:"0"`
}

func (c *AppConfig) GetBaseFrontendUrl() string {
	url := c.FrontendUrl
	if url == "" {
		url = c.BaseUrl
	}
	return url
}

type Config interface {
	Unlock(encryptionKey string) error
	Get(key string, encryptionKey string) (string, error)
	SetIgnore(key string, value string, encryptionKey string) error
	SetUpdate(key string, value string, encryptionKey string) error
	GetJWTSecret() (string, error)
	GetRelayUrls() []string
	// TrustedNwcRelay reports whether the NWC relays in GetRelayUrls are run
	// by this hub. See config.TrustedNwcRelay for what it turns on and why it
	// must never be set for a relay the operator does not control.
	TrustedNwcRelay() bool
	SetTrustedNwcRelay(trusted bool) error
	// PrivateEnvelopeLimits bounds one private-transport batch envelope,
	// resolved as: hub setting, else env var, else the SDK default.
	PrivateEnvelopeLimits() transport.Limits
	SetPrivateEnvelopeLimits(limits transport.Limits) error
	GetNetwork() string
	GetMempoolApi() string
	SetMempoolApi(value string) error
	GetEnv() *AppConfig
	CheckUnlockPassword(password string) bool
	ChangeUnlockPassword(currentUnlockPassword string, newUnlockPassword string) error
	SetAutoUnlockPassword(unlockPassword string) error
	SaveUnlockPasswordCheck(encryptionKey string) error
	SetupCompleted() bool
	GetCurrency() string
	SetCurrency(value string) error
	GetFlokicoinDisplayFormat() string
	SetFlokicoinDisplayFormat(value string) error
	GetLokihubServicesURL() string
	SetLokihubServicesURL(value string) error
	GetLokihubStoreURL() string
	SetLokihubStoreURL(value string) error
	GetSwapServiceURL() string
	SetSwapServiceURL(value string) error
	GetMessageboardNwcUrl() string
	SetMessageboardNwcUrl(value string) error
	GetRelay() string
	SetRelay(value string) error
	GetGeneralRelayUrls() []string
	GetGeneralRelay() string
	SetGeneralRelay(value string) error
	GetSearchRelayUrls() []string
	GetSearchRelay() string
	SetSearchRelay(value string) error

	EnableSwap() bool
	SetEnableSwap(value bool) error
	EnableMessageboardNwc() bool
	SetEnableMessageboardNwc(value bool) error
	GetDefaultWorkDir() string
	GetLSP() string
	SetLSP(value string) error
	GetCachedServicesJSON() string
	SetCachedServicesJSON(json string) error
}
