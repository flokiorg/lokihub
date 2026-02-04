package api

import (
	"context"
	"io"
	"time"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/lnclient"
	"github.com/flokiorg/lokihub/lsps/lsps1"
	"github.com/flokiorg/lokihub/lsps/lsps2"
	"github.com/flokiorg/lokihub/lsps/manager"
	"github.com/flokiorg/lokihub/swaps"
)

type API interface {
	CreateApp(createAppRequest *CreateAppRequest) (*CreateAppResponse, error)
	UpdateApp(app *db.App, updateAppRequest *UpdateAppRequest) error
	Transfer(ctx context.Context, fromAppId *uint, toAppId *uint, amountMloki uint64) error
	DeleteApp(app *db.App) error
	GetApp(app *db.App) *App
	ListApps(limit uint64, offset uint64, filters ListAppsFilters, orderBy string) (*ListAppsResponse, error)

	ListChannels(ctx context.Context) ([]Channel, error)

	ResetRouter(key string) error
	ChangeUnlockPassword(changeUnlockPasswordRequest *ChangeUnlockPasswordRequest) error
	SetAutoUnlockPassword(unlockPassword string) error
	Stop() error
	GetNodeConnectionInfo(ctx context.Context) (*lnclient.NodeConnectionInfo, error)
	GetNodeStatus(ctx context.Context) (*lnclient.NodeStatus, error)
	ListPeers(ctx context.Context) ([]lnclient.PeerDetails, error)
	ConnectPeer(ctx context.Context, connectPeerRequest *ConnectPeerRequest) error
	DisconnectPeer(ctx context.Context, peerId string) error
	OpenChannel(ctx context.Context, openChannelRequest *OpenChannelRequest) (*OpenChannelResponse, error)

	CloseChannel(ctx context.Context, peerId, channelId string, force bool) (*CloseChannelResponse, error)
	UpdateChannel(ctx context.Context, updateChannelRequest *UpdateChannelRequest) error

	GetNewOnchainAddress(ctx context.Context) (string, error)
	GetUnusedOnchainAddress(ctx context.Context) (string, error)
	SignMessage(ctx context.Context, message string) (*SignMessageResponse, error)
	RedeemOnchainFunds(ctx context.Context, toAddress string, amount uint64, feeRate *uint64, sendAll bool) (*RedeemOnchainFundsResponse, error)
	GetBalances(ctx context.Context) (*BalancesResponse, error)
	ListTransactions(ctx context.Context, appId *uint, limit uint64, offset uint64) (*ListTransactionsResponse, error)
	ListOnchainTransactions(ctx context.Context, limit, offset uint64) ([]lnclient.OnchainTransaction, error)
	SendPayment(ctx context.Context, invoice string, amountMloki *uint64, metadata map[string]interface{}) (*SendPaymentResponse, error)
	CreateInvoice(ctx context.Context, req *MakeInvoiceRequest) (*MakeInvoiceResponse, error)
	LookupInvoice(ctx context.Context, paymentHash string) (*LookupInvoiceResponse, error)
	RequestMempoolApi(ctx context.Context, endpoint string) (interface{}, error)
	GetServices(ctx context.Context) (interface{}, error)
	GetInfo(ctx context.Context) (*InfoResponse, error)
	GetMnemonic(unlockPassword string) (*MnemonicResponse, error)
	SetNextBackupReminder(backupReminderRequest *BackupReminderRequest) error
	Start(startRequest *StartRequest) error
	Setup(ctx context.Context, setupRequest *SetupRequest) error
	SetupLocal(ctx context.Context, setupRequest *SetupLocalRequest) error
	SetupManual(ctx context.Context, setupRequest *SetupManualRequest) error
	GetSetupStatus(ctx context.Context) (*SetupStatusResponse, error)
	SendPaymentProbes(ctx context.Context, sendPaymentProbesRequest *SendPaymentProbesRequest) (*SendPaymentProbesResponse, error)
	SendSpontaneousPaymentProbes(ctx context.Context, sendSpontaneousPaymentProbesRequest *SendSpontaneousPaymentProbesRequest) (*SendSpontaneousPaymentProbesResponse, error)
	GetNetworkGraph(ctx context.Context, nodeIds []string) (NetworkGraphResponse, error)
	SyncWallet() error
	GetLogOutput(ctx context.Context, logType string, getLogRequest *GetLogOutputRequest) (*GetLogOutputResponse, error)

	CreateBackup(unlockPassword string, w io.Writer) error
	RestoreBackup(unlockPassword string, r io.Reader) error
	MigrateNodeStorage(ctx context.Context, to string) error
	GetWalletCapabilities(ctx context.Context) (*WalletCapabilitiesResponse, error)
	Health(ctx context.Context) (*HealthResponse, error)
	SetCurrency(currency string) error
	SetFlokicoinDisplayFormat(format string) error
	UpdateSettings(updateSettingsRequest *UpdateSettingsRequest) error
	LookupSwap(swapId string) (*LookupSwapResponse, error)
	ListSwaps() (*ListSwapsResponse, error)
	GetSwapInInfo() (*SwapInfoResponse, error)
	GetSwapOutInfo() (*SwapInfoResponse, error)
	InitiateSwapIn(ctx context.Context, initiateSwapInRequest *InitiateSwapRequest) (*swaps.SwapResponse, error)
	InitiateSwapOut(ctx context.Context, initiateSwapOutRequest *InitiateSwapRequest) (*swaps.SwapResponse, error)
	RefundSwap(refundSwapRequest *RefundSwapRequest) error
	GetSwapMnemonic() string
	GetAutoSwapConfig() (*GetAutoSwapConfigResponse, error)
	EnableAutoSwapOut(ctx context.Context, autoSwapRequest *EnableAutoSwapRequest) error
	DisableAutoSwap() error
	SetNodeAlias(ctx context.Context, nodeAlias string) error
	GetCustomNodeCommands() (*CustomNodeCommandsResponse, error)
	ExecuteCustomNodeCommand(ctx context.Context, command string) (interface{}, error)
	SendEvent(event string, properties interface{})
	GetForwards() (*GetForwardsResponse, error)

	// LSPS
	LSPS0ListProtocols(ctx context.Context, req *LSPS0ListProtocolsRequest) (*LSPS0ListProtocolsResponse, error)
	LSPS1GetInfo(ctx context.Context, req *LSPS1GetInfoRequest) (interface{}, error) // placeholder return type for now using generic interface or specific if needed
	LSPS1CreateOrder(ctx context.Context, req *LSPS1CreateOrderRequest) (interface{}, error)
	LSPS1GetOrder(ctx context.Context, req *LSPS1GetOrderRequest) (interface{}, error)
	LSPS2GetInfo(ctx context.Context, req *LSPS2GetInfoRequest) (interface{}, error) // Returns OpeningFeeParams
	LSPS2Buy(ctx context.Context, req *LSPS2BuyRequest) (*LSPS2BuyResponse, error)
	LSPS5SetWebhook(ctx context.Context, req *LSPS5SetWebhookRequest) (interface{}, error)
	LSPS5ListWebhooks(ctx context.Context, req *LSPS5ListWebhooksRequest) (interface{}, error)
	LSPS5RemoveWebhook(ctx context.Context, req *LSPS5RemoveWebhookRequest) (interface{}, error)
	UpdateLSPS1OrderState(ctx context.Context, orderID, state string) error
	LSPS1ListOrders(ctx context.Context) (*LSPS1ListOrdersResponse, error)

	// LSP Management
	HandleListLSPs(ctx context.Context) ([]manager.SettingsLSP, error)
	HandleAddLSP(ctx context.Context, req *AddLSPRequest) (*manager.SettingsLSP, error)
	HandleUpdateLSP(ctx context.Context, pubkey string, req *UpdateLSPRequest) error
	HandleDeleteLSP(ctx context.Context, pubkey string) error

	// Legacy wrappers (to be deprecated)
	ListLSPs() ([]manager.SettingsLSP, error)
	GetSelectedLSPs() ([]manager.SettingsLSP, error)
	AddSelectedLSP(pubkey string) error
	RemoveSelectedLSP(pubkey string) error
	AddLSP(name, uri string) error
	RemoveLSP(pubkey string) error

	// Invoice Fee Estimation
	EstimateInvoiceFee(ctx context.Context, invoice string) (uint64, error)
}

type App struct {
	ID                 uint       `json:"id"`
	Name               string     `json:"name"`
	Description        string     `json:"description"`
	AppPubkey          string     `json:"appPubkey"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	LastUsedAt         *time.Time `json:"lastUsedAt"`
	ExpiresAt          *time.Time `json:"expiresAt"`
	Scopes             []string   `json:"scopes"`
	MaxAmountLoki      uint64     `json:"maxAmount"`
	BudgetUsage        uint64     `json:"budgetUsage"`
	BudgetRenewal      string     `json:"budgetRenewal"`
	Isolated           bool       `json:"isolated"`
	WalletPubkey       string     `json:"walletPubkey"`
	UniqueWalletPubkey bool       `json:"uniqueWalletPubkey"`
	Balance            int64      `json:"balance"`
	Metadata           Metadata   `json:"metadata,omitempty"`
}

type ListAppsFilters struct {
	Name          string `json:"name"`
	AppStoreAppId string `json:"appStoreAppId"`
	Unused        bool   `json:"unused"`
	SubWallets    *bool  `json:"subWallets"`
}

type ListAppsResponse struct {
	Apps       []App  `json:"apps"`
	TotalCount uint64 `json:"totalCount"`
}

type UpdateAppRequest struct {
	Name            *string   `json:"name"`
	MaxAmountLoki   *uint64   `json:"maxAmount"`
	BudgetRenewal   *string   `json:"budgetRenewal"`
	ExpiresAt       *string   `json:"expiresAt"`
	UpdateExpiresAt bool      `json:"updateExpiresAt"`
	Scopes          []string  `json:"scopes"`
	Metadata        *Metadata `json:"metadata"`
	Isolated        *bool     `json:"isolated"`
}

type TransferRequest struct {
	AmountLoki uint64 `json:"amountLoki"`
	FromAppId  *uint  `json:"fromAppId"`
	ToAppId    *uint  `json:"toAppId"`
}

type CreateAppRequest struct {
	Name           string   `json:"name"`
	Pubkey         string   `json:"pubkey"`
	MaxAmountLoki  uint64   `json:"maxAmount"`
	BudgetRenewal  string   `json:"budgetRenewal"`
	ExpiresAt      string   `json:"expiresAt"`
	Scopes         []string `json:"scopes"`
	ReturnTo       string   `json:"returnTo"`
	Isolated       bool     `json:"isolated"`
	Metadata       Metadata `json:"metadata,omitempty"`
	UnlockPassword string   `json:"unlockPassword"`
}

type CreateLightningAddressRequest struct {
	Address string `json:"address"`
	AppId   uint   `json:"appId"`
}

type InitiateSwapRequest struct {
	SwapAmount  uint64 `json:"swapAmount"`
	Destination string `json:"destination"`
}

type RefundSwapRequest struct {
	SwapId  string `json:"swapId"`
	Address string `json:"address"`
}

type EnableAutoSwapRequest struct {
	BalanceThreshold uint64 `json:"balanceThreshold"`
	SwapAmount       uint64 `json:"swapAmount"`
	Destination      string `json:"destination"`
}

type GetAutoSwapConfigResponse struct {
	Type             string `json:"type"`
	Enabled          bool   `json:"enabled"`
	BalanceThreshold uint64 `json:"balanceThreshold"`
	SwapAmount       uint64 `json:"swapAmount"`
	Destination      string `json:"destination"`
}

type SwapInfoResponse struct {
	LokiServiceFee  float64 `json:"lokiServiceFee"`
	BoltzServiceFee float64 `json:"boltzServiceFee"`
	BoltzNetworkFee uint64  `json:"boltzNetworkFee"`
	MinAmount       uint64  `json:"minAmount"`
	MaxAmount       uint64  `json:"maxAmount"`
}

type ListSwapsResponse struct {
	Swaps []Swap `json:"swaps"`
}

type LookupSwapResponse = Swap

type Swap struct {
	Id                 string `json:"id"`
	Type               string `json:"type"`
	State              string `json:"state"`
	Invoice            string `json:"invoice"`
	SendAmount         uint64 `json:"sendAmount"`
	ReceiveAmount      uint64 `json:"receiveAmount"`
	PaymentHash        string `json:"paymentHash"`
	DestinationAddress string `json:"destinationAddress"`
	RefundAddress      string `json:"refundAddress"`
	LockupAddress      string `json:"lockupAddress"`
	LockupTxId         string `json:"lockupTxId"`
	ClaimTxId          string `json:"claimTxId"`
	AutoSwap           bool   `json:"autoSwap"`
	BoltzPubkey        string `json:"boltzPubkey"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
	UsedXpub           bool   `json:"usedXpub"`
}

type StartRequest struct {
	UnlockPassword string `json:"unlockPassword"`
}

type UnlockRequest struct {
	UnlockPassword  string  `json:"unlockPassword"`
	TokenExpiryDays *uint64 `json:"tokenExpiryDays"`
	Permission      string  `json:"permission,omitempty"` // "full" or "readonly"
}

type BackupReminderRequest struct {
	NextBackupReminder string `json:"nextBackupReminder"`
}

type SendEventRequest struct {
	Event      string      `json:"event"`
	Properties interface{} `json:"properties"`
}

type SetupRequest struct {
	LNBackendType  string `json:"backendType"`
	UnlockPassword string `json:"unlockPassword"`

	Mnemonic           string `json:"mnemonic"`
	NextBackupReminder string `json:"nextBackupReminder"`

	// AutoConnect logic
	AutoConnect bool `json:"autoConnect"`
	// CustomConfig logic
	CustomConfig *CustomConfig `json:"customConfig"`

	// FLND fields
	LNDAddress      string `json:"lndAddress"`
	LNDCertFile     string `json:"lndCertFile"`
	LNDMacaroonFile string `json:"lndMacaroonFile"`
	LNDCertHex      string `json:"lndCertHex"`
	LNDMacaroonHex  string `json:"lndMacaroonHex"`

	LokihubServicesURL    string `json:"lokihubServicesURL"`
	SwapServiceUrl        string `json:"swapServiceUrl"`
	Relay                 string `json:"relay"`
	MessageboardNwcUrl    string `json:"messageboardNwcUrl"`
	MempoolApi            string `json:"mempoolApi"`
	LSP                   string `json:"lsp"`
	EnableSwap            *bool  `json:"enableSwap"`
	EnableMessageboardNwc *bool  `json:"enableMessageboardNwc"`
}

type SetupLocalRequest struct {
	UnlockPassword        string            `json:"unlockPassword"`
	LokihubServicesURL    string            `json:"lokihubServicesURL"`
	SwapServiceUrl        string            `json:"swapServiceUrl"`
	Relay                 string            `json:"relay"`
	MessageboardNwcUrl    string            `json:"messageboardNwcUrl"`
	MempoolApi            string            `json:"mempoolApi"`
	LSP                   string            `json:"lsp"` // Deprecated: Use LSPs instead
	LSPs                  []LSPSettingInput `json:"lsps,omitempty"`
	EnableMessageboardNwc *bool             `json:"enableMessageboardNwc"`
}

type SetupManualRequest struct {
	UnlockPassword        string            `json:"unlockPassword"`
	LNDAddress            string            `json:"lndAddress"`
	LNDCertHex            string            `json:"lndCertHex"`
	LNDMacaroonHex        string            `json:"lndMacaroonHex"`
	LokihubServicesURL    string            `json:"lokihubServicesURL"`
	SwapServiceUrl        string            `json:"swapServiceUrl"`
	Relay                 string            `json:"relay"`
	MessageboardNwcUrl    string            `json:"messageboardNwcUrl"`
	MempoolApi            string            `json:"mempoolApi"`
	LSP                   string            `json:"lsp"` // Deprecated: Use LSPs instead
	LSPs                  []LSPSettingInput `json:"lsps,omitempty"`
	EnableMessageboardNwc *bool             `json:"enableMessageboardNwc"`
}

type SetupStatusResponse struct {
	Active bool `json:"active"`
}

type CustomConfig struct {
	DataDir   string `json:"datadir"`
	RpcListen string `json:"rpcListen"`
}

type CreateAppResponse struct {
	PairingUri    string   `json:"pairingUri"`
	PairingSecret string   `json:"pairingSecretKey"`
	Pubkey        string   `json:"pairingPublicKey"`
	RelayUrls     []string `json:"relayUrls"`
	WalletPubkey  string   `json:"walletPubkey"`
	Lud16         string   `json:"lud16"`
	Id            uint     `json:"id"`
	Name          string   `json:"name"`
	ReturnTo      string   `json:"returnTo"`
}

type User struct {
	Email string `json:"email"`
}

type InfoResponseRelay struct {
	Url    string `json:"url"`
	Online bool   `json:"online"`
}

type LSPInfo struct {
	Name    string `json:"name"`
	Pubkey  string `json:"pubkey"`
	Host    string `json:"host"`
	Website string `json:"website"`
	Active  bool   `json:"active"`
}

type InfoResponse struct {
	BackendType                 string              `json:"backendType"`
	SetupCompleted              bool                `json:"setupCompleted"`
	Running                     bool                `json:"running"`
	Unlocked                    bool                `json:"unlocked"`
	Version                     string              `json:"version"`
	Network                     string              `json:"network"`
	StartupState                string              `json:"startupState"`
	StartupError                string              `json:"startupError"`
	StartupErrorTime            time.Time           `json:"startupErrorTime"`
	AutoUnlockPasswordSupported bool                `json:"autoUnlockPasswordSupported"`
	AutoUnlockPasswordEnabled   bool                `json:"autoUnlockPasswordEnabled"`
	Currency                    string              `json:"currency"`
	FlokicoinDisplayFormat      string              `json:"flokicoinDisplayFormat"`
	Relays                      []InfoResponseRelay `json:"relays"`
	Relay                       string              `json:"relay"`
	NodeAlias                   string              `json:"nodeAlias"`
	MempoolUrl                  string              `json:"mempoolUrl"`
	LSPs                        []LSPInfo           `json:"lsps"`
	LokihubServicesURL          string              `json:"lokihubServicesURL"`
	SwapServiceUrl              string              `json:"swapServiceUrl"`
	MessageboardNwcUrl          string              `json:"messageboardNwcUrl"`
	EnableSwap                  bool                `json:"enableSwap"`
	EnableMessageboardNwc       bool                `json:"enableMessageboardNwc"`
	WorkDir                     string              `json:"workDir"`
	EnablePolling               bool                `json:"enablePolling"`
}

type UpdateSettingsRequest struct {
	Currency               string            `json:"currency"`
	FlokicoinDisplayFormat string            `json:"flokicoinDisplayFormat"`
	LokihubServicesURL     string            `json:"lokihubServicesURL"`
	SwapServiceUrl         string            `json:"swapServiceUrl"`
	Relay                  string            `json:"relay"`
	MessageboardNwcUrl     string            `json:"messageboardNwcUrl"`
	MempoolApi             string            `json:"mempoolApi"`
	LSPs                   []LSPSettingInput `json:"lsps,omitempty"`
	EnableSwap             *bool             `json:"enableSwap"`
	EnableMessageboardNwc  *bool             `json:"enableMessageboardNwc"`
}

type LSPSettingInput struct {
	Name        string `json:"name"`
	Pubkey      string `json:"pubkey"`
	Host        string `json:"host"`
	Active      bool   `json:"active"`
	IsCommunity bool   `json:"isCommunity"`
}

type SetNodeAliasRequest struct {
	NodeAlias string `json:"nodeAlias"`
}

type MnemonicRequest struct {
	UnlockPassword string `json:"unlockPassword"`
}

type MnemonicResponse struct {
	Mnemonic string `json:"mnemonic"`
}

type ChangeUnlockPasswordRequest struct {
	CurrentUnlockPassword string `json:"currentUnlockPassword"`
	NewUnlockPassword     string `json:"newUnlockPassword"`
}
type AutoUnlockRequest struct {
	UnlockPassword string `json:"unlockPassword"`
}

type ConnectPeerRequest = lnclient.ConnectPeerRequest
type OpenChannelRequest = lnclient.OpenChannelRequest
type OpenChannelResponse = lnclient.OpenChannelResponse
type CloseChannelResponse = lnclient.CloseChannelResponse
type UpdateChannelRequest = lnclient.UpdateChannelRequest

type RedeemOnchainFundsRequest struct {
	ToAddress string  `json:"toAddress"`
	Amount    uint64  `json:"amount"`
	FeeRate   *uint64 `json:"feeRate"`
	SendAll   bool    `json:"sendAll"`
}

type RedeemOnchainFundsResponse struct {
	TxId string `json:"txId"`
}

type OnchainBalanceResponse = lnclient.OnchainBalanceResponse
type BalancesResponse = lnclient.BalancesResponse

type SendPaymentResponse = Transaction
type MakeInvoiceResponse = Transaction
type LookupInvoiceResponse = Transaction

type ListTransactionsResponse struct {
	TotalCount   uint64        `json:"totalCount"`
	Transactions []Transaction `json:"transactions"`
}

// TODO: camelCase
type Transaction struct {
	Type            string      `json:"type"`
	State           string      `json:"state"`
	Invoice         string      `json:"invoice"`
	Description     string      `json:"description"`
	DescriptionHash string      `json:"descriptionHash"`
	Preimage        *string     `json:"preimage"`
	PaymentHash     string      `json:"paymentHash"`
	Amount          uint64      `json:"amount"`
	FeesPaid        uint64      `json:"feesPaid"`
	UpdatedAt       string      `json:"updatedAt"`
	CreatedAt       string      `json:"createdAt"`
	SettledAt       *string     `json:"settledAt"`
	AppId           *uint       `json:"appId"`
	Metadata        Metadata    `json:"metadata,omitempty"`
	Boostagram      *Boostagram `json:"boostagram,omitempty"`
	FailureReason   string      `json:"failureReason"`
}

type Metadata = map[string]interface{}

type Boostagram struct {
	AppName         string `json:"appName"`
	Name            string `json:"name"`
	Podcast         string `json:"podcast"`
	URL             string `json:"url"`
	Episode         string `json:"episode,omitempty"`
	FeedId          string `json:"feedId,omitempty"`
	ItemId          string `json:"itemId,omitempty"`
	Timestamp       int64  `json:"ts,omitempty"`
	Message         string `json:"message,omitempty"`
	SenderId        string `json:"senderId"`
	SenderName      string `json:"senderName"`
	Time            string `json:"time"`
	Action          string `json:"action"`
	ValueMlokiTotal int64  `json:"valueMlokiTotal"`
}

// debug api
type SendPaymentProbesRequest struct {
	Invoice string `json:"invoice"`
}

type SendPaymentProbesResponse struct {
	Error string `json:"error"`
}

type SendSpontaneousPaymentProbesRequest struct {
	Amount uint64 `json:"amount"`
	NodeId string `json:"nodeId"`
}

type SendSpontaneousPaymentProbesResponse struct {
	Error string `json:"error"`
}

const (
	LogTypeNode = "node"
	LogTypeApp  = "app"
)

type GetLogOutputRequest struct {
	MaxLen int `query:"maxLen"`
}

type GetLogOutputResponse struct {
	Log string `json:"logs"`
}

type SignMessageRequest struct {
	Message string `json:"message"`
}

type SignMessageResponse struct {
	Message   string `json:"message"`
	Signature string `json:"signature"`
}

// LSPS Types

// LSPS0
type LSPS0ListProtocolsRequest struct {
	LSPPubkey string `json:"lspPubkey"`
}

type LSPS0ListProtocolsResponse struct {
	Protocols []int `json:"protocols"`
}

// LSPS1
type LSPS1GetInfoRequest struct {
	LSPPubkey string `json:"lspPubkey"`
	Token     string `json:"token,omitempty"`
}

type LSPS1CreateOrderRequest struct {
	LSPPubkey            string                  `json:"lsp_pubkey"`
	LSPBalanceLoki       uint64                  `json:"amount_loki"`
	ClientBalanceLoki    uint64                  `json:"client_balance_loki"`
	ChannelExpiryBlocks  uint32                  `json:"channel_expiry_blocks"`
	Token                *string                 `json:"token,omitempty"`
	RefundOnchainAddress *string                 `json:"refund_onchain_address,omitempty"`
	AnnounceChannel      bool                    `json:"announce_channel"`
	OpeningFeeParams     *lsps1.OpeningFeeParams `json:"opening_fee_params,omitempty"`
}

type LSPS1GetOrderRequest struct {
	LSPPubkey string `json:"lspPubkey"`
	OrderID   string `json:"orderId"`
	Token     string `json:"token,omitempty"`
}

type LSPS1ListOrdersResponse struct {
	Orders []LSPS1Order `json:"orders"`
}

type LSPS1Order struct {
	OrderID           string    `json:"orderId"`
	LSPPubkey         string    `json:"lspPubkey"`
	State             string    `json:"state"`
	PaymentInvoice    string    `json:"paymentInvoice"`
	FeeTotal          uint64    `json:"feeTotal"`
	OrderTotal        uint64    `json:"orderTotal"`
	LSPBalanceLoki    uint64    `json:"lspBalanceLoki"`
	ClientBalanceLoki uint64    `json:"clientBalanceLoki"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// LSPS2
type LSPS2GetInfoRequest struct {
	LSPPubkey string `json:"lspPubkey"`
	Token     string `json:"token,omitempty"`
}

// LSPS2OpeningFeeParams mirrors lsps2.OpeningFeeParams but for API usage if needed,
// strictly we can reuse the type from lsps2 package if we import it,
// but usually API types are separate. However, to avoid duplication and alignment issues,
// we can embed or use the lsps2 type if it serializes to compatible JSON.
// The lsps2.OpeningFeeParams has JSON tags, so we can use it.
type LSPS2OpeningFeeParams = lsps2.OpeningFeeParams

type LSPS2BuyRequest struct {
	LSPPubkey        string                `json:"lspPubkey"`
	PaymentSizeMloki *uint64               `json:"paymentSizeMloki,omitempty"`
	OpeningFeeParams LSPS2OpeningFeeParams `json:"openingFeeParams"`
}

type LSPS2BuyResponse struct {
	RequestID       string `json:"requestId"`
	InterceptSCID   uint64 `json:"interceptScid,string"`
	CLTVExpiryDelta uint16 `json:"cltvExpiryDelta"`
	LSPNodeID       string `json:"lspNodeID"`
}

// LSPS5
type LSPS5SetWebhookRequest struct {
	LSPPubkey string   `json:"lspPubkey"`
	URL       string   `json:"url"`
	Events    []string `json:"events"`
	Signature string   `json:"signature"`
	Transport string   `json:"transport,omitempty"`
}

type LSPS5ListWebhooksRequest struct {
	LSPPubkey string `json:"lspPubkey"`
}

type LSPS5RemoveWebhookRequest struct {
	LSPPubkey string `json:"lspPubkey"`
	URL       string `json:"url"`
}

type PayInvoiceRequest struct {
	Amount   *uint64  `json:"amount"`
	Metadata Metadata `json:"metadata"`
}

type MakeOfferRequest struct {
	Description string `json:"description"`
}

type MakeInvoiceRequest struct {
	Amount                       uint64 `json:"amount"`
	Description                  string `json:"description"`
	LSPJitChannelSCID            string `json:"lspJitChannelSCID,omitempty"`
	LSPCltvExpiryDelta           uint16 `json:"lspCltvExpiryDelta,omitempty"`
	LSPPubkey                    string `json:"lspPubkey,omitempty"`
	LSPFeeBaseMloki              uint64 `json:"lspFeeBaseMloki,omitempty"`
	LSPFeeProportionalMillionths uint32 `json:"lspFeeProportionalMillionths,omitempty"`
}

type ResetRouterRequest struct {
	Key string `json:"key"`
}

type BasicBackupRequest struct {
	UnlockPassword string `json:"unlockPassword"`
}

type BasicRestoreWailsRequest struct {
	UnlockPassword string `json:"unlockPassword"`
}

type NetworkGraphResponse = lnclient.NetworkGraphResponse

type WalletCapabilitiesResponse struct {
	Scopes            []string `json:"scopes"`
	Methods           []string `json:"methods"`
	NotificationTypes []string `json:"notificationTypes"`
}

type Channel struct {
	LocalBalance                             int64       `json:"localBalance"`
	LocalSpendableBalance                    int64       `json:"localSpendableBalance"`
	RemoteBalance                            int64       `json:"remoteBalance"`
	Id                                       string      `json:"id"`
	RemotePubkey                             string      `json:"remotePubkey"`
	FundingTxId                              string      `json:"fundingTxId"`
	FundingTxVout                            uint32      `json:"fundingTxVout"`
	Active                                   bool        `json:"active"`
	Public                                   bool        `json:"public"`
	InternalChannel                          interface{} `json:"internalChannel"`
	Confirmations                            *uint32     `json:"confirmations"`
	ConfirmationsRequired                    *uint32     `json:"confirmationsRequired"`
	ForwardingFeeBaseMloki                   uint32      `json:"forwardingFeeBaseMloki"`
	ForwardingFeeProportionalMillionths      uint32      `json:"forwardingFeeProportionalMillionths"`
	UnspendablePunishmentReserve             uint64      `json:"unspendablePunishmentReserve"`
	CounterpartyUnspendablePunishmentReserve uint64      `json:"counterpartyUnspendablePunishmentReserve"`
	Error                                    *string     `json:"error"`
	Status                                   string      `json:"status"`
	IsOutbound                               bool        `json:"isOutbound"`
}

type MigrateNodeStorageRequest struct {
	To string `json:"to"`
}

type HealthAlarmKind string

const (
	HealthAlarmKindNodeNotReady      HealthAlarmKind = "node_not_ready"
	HealthAlarmKindChannelsOffline   HealthAlarmKind = "channels_offline"
	HealthAlarmKindNostrRelayOffline HealthAlarmKind = "nostr_relay_offline"
)

type HealthAlarm struct {
	Kind       HealthAlarmKind `json:"kind"`
	RawDetails any             `json:"rawDetails,omitempty"`
}

func NewHealthAlarm(kind HealthAlarmKind, rawDetails any) HealthAlarm {
	return HealthAlarm{
		Kind:       kind,
		RawDetails: rawDetails,
	}
}

type HealthResponse struct {
	Alarms []HealthAlarm `json:"alarms,omitempty"`
}

type CustomNodeCommandArgDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type CustomNodeCommandDef struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Args        []CustomNodeCommandArgDef `json:"args"`
}

type CustomNodeCommandsResponse struct {
	Commands []CustomNodeCommandDef `json:"commands"`
}

type ExecuteCustomNodeCommandRequest struct {
	Command string `json:"command"`
}

type GetForwardsResponse struct {
	OutboundAmountForwardedMloki uint64 `json:"outboundAmountForwardedMloki"`
	TotalFeeEarnedMloki          uint64 `json:"totalFeeEarnedMloki"`
	NumForwards                  uint64 `json:"numForwards"`
}
