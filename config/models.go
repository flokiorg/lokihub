package config

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

	// JITWalletRateLimitPerHour caps create_jit_wallet calls per calling app
	// pubkey. 0 disables the limit entirely (useful for dev/integration testing
	// against a shared long-lived hub).
	JITWalletRateLimitPerHour int `envconfig:"JIT_WALLET_RATE_LIMIT_PER_HOUR" default:"10"`
	// JITWalletClaimRateLimitPerHour caps claim_funds calls per calling jit_wallet
	// app pubkey (separate limiter from JITWalletRateLimitPerHour). 0 disables
	// the limit entirely.
	JITWalletClaimRateLimitPerHour int `envconfig:"JIT_WALLET_CLAIM_RATE_LIMIT_PER_HOUR" default:"20"`
	// CircleWalletRateLimitPerHour caps create_circle_wallet calls per calling
	// app pubkey. 0 disables the limit entirely.
	CircleWalletRateLimitPerHour int `envconfig:"CIRCLE_WALLET_RATE_LIMIT_PER_HOUR" default:"3"`
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
