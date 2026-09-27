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
	// CashWalletClaimRateLimitPerHour caps cash_redeem calls per calling cash_wallet
	// app pubkey (separate limiter from CashWalletRateLimitPerHour). 0 disables
	// the limit entirely.
	CashWalletClaimRateLimitPerHour int `envconfig:"CASH_WALLET_CLAIM_RATE_LIMIT_PER_HOUR" default:"20"`
	// CircleWalletRateLimitPerHour caps create_circle_wallet calls per calling
	// app pubkey. 0 disables the limit entirely.
	CircleWalletRateLimitPerHour int `envconfig:"CIRCLE_WALLET_RATE_LIMIT_PER_HOUR" default:"3"`

	// PrivateTransportEnabled turns on the wrapped private transport alongside the
	// standard kind-23194 one. Defaults false while the receive path is
	// incomplete — see config.PrivateTransportEnabled.
	PrivateTransportEnabled bool `envconfig:"PRIVATE_TRANSPORT_ENABLED" default:"false"`

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
	// PrivateTransportEnabled reports whether this hub serves the wrapped private
	// transport. Defaults false; see config.PrivateTransportEnabled for why.
	PrivateTransportEnabled() bool
	SetPrivateTransportEnabled(enabled bool) error
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
