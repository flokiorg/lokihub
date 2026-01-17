package config

const (
	LNDBackendType = "FLND"
)

const (
	OnchainAddressKey           = "OnchainAddress"
	AutoSwapBalanceThresholdKey = "AutoSwapBalanceThreshold"
	AutoSwapAmountKey           = "AutoSwapAmount"
	AutoSwapDestinationKey      = "AutoSwapDestination"
	AutoSwapXpubIndexStart      = "AutoSwapXpubIndexStart"
)

type AppConfig struct {
	Relay           string `envconfig:"RELAY"`
	LNBackendType   string `envconfig:"LN_BACKEND_TYPE"`
	LNDAddress      string `envconfig:"LND_ADDRESS"`
	LNDCertFile     string `envconfig:"LND_CERT_FILE"`
	LNDMacaroonFile string `envconfig:"LND_MACAROON_FILE"`
	Workdir         string `envconfig:"WORK_DIR"`
	Port            string `envconfig:"PORT" default:"1610"`
	DatabaseUri     string `envconfig:"DATABASE_URI" default:"nwc.db"`
	LogLevel        string `envconfig:"LOG_LEVEL" default:"4"`
	LogToFile       bool   `envconfig:"LOG_TO_FILE" default:"true"`
	Network         string `envconfig:"NETWORK"`
	MempoolApi      string `envconfig:"MEMPOOL_API"`
	BaseUrl         string `envconfig:"BASE_URL"`
	FrontendUrl     string `envconfig:"FRONTEND_URL"`

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

	EnableSwap() bool
	SetEnableSwap(value bool) error
	EnableMessageboardNwc() bool
	SetEnableMessageboardNwc(value bool) error
	GetDefaultWorkDir() string
	GetLSP() string
	SetLSP(value string) error
}
