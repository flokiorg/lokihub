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
	ReplaceCircleAllowlist(app *db.App, pubkeys []string) error
	RemoveCircleAllowedPubkey(app *db.App, pubkey string) error
	RefreshCircleAllowlist(ctx context.Context, app *db.App) error
	// PreviewCircleRefresh re-fetches the provider's kind:3 contacts and reports
	// how they'd differ from the currently-stored allowlist, without applying
	// anything — lets a caller show the delta and get confirmation before
	// calling RefreshCircleAllowlist to actually replace the list.
	PreviewCircleRefresh(ctx context.Context, app *db.App) (*CircleRefreshPreview, error)
	ListCircleAllowlist(app *db.App) ([]string, error)
	// ListCircleChildrenBalances returns a page of a circle_hub's circle_wallet
	// children, each with its current isolated balance. limit == 0 returns
	// every child unpaginated (used by the pre-delete confirmation UI).
	ListCircleChildrenBalances(app *db.App, limit uint64, offset uint64) ([]CircleChildBalance, uint64, error)
	DeleteCircleHub(app *db.App, mode string) (*DeleteCircleHubResult, error)
	// DeleteCircleWalletChild removes a single circle_wallet child in any state
	// (empty or with a remaining balance), unlike DeleteCircleHub which only
	// operates on the whole hub at once.
	DeleteCircleWalletChild(hubAppID uint, childAppID uint) error
	// ListCircleIdentities returns every CircleIdentity, for the circle-creation-time picker.
	ListCircleIdentities() ([]CircleIdentitySummary, error)
	// GetCircleIdentity returns identity details plus policy-specific counts and
	// (for allowlist policy) the full pubkey list, and how many circle_hub
	// apps currently reference it.
	GetCircleIdentity(ctx context.Context, id uint) (*CircleIdentityResponse, error)
	// DeleteCircleIdentity removes a standalone CircleIdentity — refuses
	// (ErrInvalidParams) if any circle_hub app still references it.
	DeleteCircleIdentity(id uint) error
	// GetApp returns full detail for a single app — for a circle_hub app with
	// a following-policy identity, this may cold-fetch the follower count
	// once (single deliberate target, like GetCircleIdentity), unlike ListApps
	// which never blocks on a whole page of apps.
	GetApp(ctx context.Context, app *db.App) *App
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
	SendPayment(ctx context.Context, invoice string, amountMloki *uint64, appID *uint, metadata map[string]interface{}) (*SendPaymentResponse, error)
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
	// UpdateLSPS1OrderState updates orderID's state on behalf of lspPubkey.
	// Rejects the update if orderID's persisted LSPPubkey doesn't match
	// lspPubkey — the caller (an LSPS5 webhook notification) only proves it
	// controls lspPubkey, not that it's the LSP that actually owns this order.
	UpdateLSPS1OrderState(ctx context.Context, lspPubkey, orderID, state string) error
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

	// JIT wallets / claims
	// ListJITWalletClaims returns a page of a jit_hub's recipient slices
	// (one row per JITWalletClaim, across every jit_wallet child). limit == 0
	// returns every row unpaginated. status filters by JITAllocationStatus*
	// ("" means unfiltered); counts in the returned JITWalletClaimCounts
	// always reflect the full, unfiltered set so a UI can show per-tab totals
	// regardless of which tab is selected.
	ListJITWalletClaims(appID uint, limit uint64, offset uint64, status string) ([]JITWalletClaimResponse, uint64, JITWalletClaimCounts, error)
	// CreateJITWallet creates, funds, and reveals a shared JIT wallet serving
	// every recipient in the request in one shot — the admin equivalent of a
	// beneficiary calling create_jit_wallet over NWC.
	CreateJITWallet(hubID uint, req *CreateJITWalletRequest) (*CreateJITWalletResponse, error)
	// DeleteJITWalletClaim removes an unclaimed slice, sweeping its amount
	// back to the hub. To delete the whole wallet (all its slices), use
	// DeleteJITWallet instead.
	DeleteJITWalletClaim(walletAppID uint, claimID uint) error
	// DeleteJITWallet reclaims any remaining balance back to the hub and
	// deletes a jit_wallet child, regardless of how much of it has been spent.
	DeleteJITWallet(hubAppID uint, walletAppID uint) error
	GetJITWalletConnection(appID uint) (*JITWalletConnectionResponse, error)
	// GetJITWalletRecipients returns every recipient slice of a single
	// jit_wallet (claimed or not), scoped by the wallet's own app ID rather
	// than its parent hub's — for a jit_wallet's own AppDetails page to show
	// who it serves, which may be more than one beneficiary.
	GetJITWalletRecipients(appID uint) ([]JITWalletClaimResponse, error)

	// Identity Authority registry
	ListIdentityAuthorities() ([]IdentityAuthorityResponse, error)
	AddIdentityAuthority(req *AddIdentityAuthorityRequest) (*IdentityAuthorityResponse, error)
	DeleteIdentityAuthority(pubkey string) error
}

type App struct {
	ID            uint       `json:"id"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	AppPubkey     string     `json:"appPubkey"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	LastUsedAt    *time.Time `json:"lastUsedAt"`
	ExpiresAt     *time.Time `json:"expiresAt"`
	Scopes        []string   `json:"scopes"`
	MaxAmountLoki uint64     `json:"maxAmount"`
	BudgetUsage   uint64     `json:"budgetUsage"`
	BudgetRenewal string     `json:"budgetRenewal"`
	Kind          string     `json:"kind"`
	// Isolated is derived from Kind (db.App.IsIsolated) — true for every kind
	// that maintains its own balance (isolated, jit_hub, jit_wallet,
	// circle_hub, circle_wallet). Kept as an explicit field because the
	// frontend gates isolated-balance UI (e.g. the increase/decrease buttons)
	// on it directly rather than duplicating the kind list client-side.
	Isolated           bool     `json:"isolated"`
	WalletPubkey       string   `json:"walletPubkey"`
	UniqueWalletPubkey bool     `json:"uniqueWalletPubkey"`
	Balance            int64    `json:"balance"`
	Metadata           Metadata `json:"metadata,omitempty"`
	// CircleIdentity is set only for circle_hub apps — a lightweight
	// summary of the attached identity plus policy-specific counts, so the
	// frontend Circles card doesn't need an extra round-trip per app.
	CircleIdentity *CircleIdentitySummaryWithCounts `json:"circleIdentity,omitempty"`
	// JITPerWalletMaxMloki/JITMaxExpSecs are set only for jit_hub apps — the
	// hub-wide defaults set at creation time, surfaced here so Edit Connection
	// can display and update them instead of only being settable once.
	JITPerWalletMaxMloki *int `json:"jitPerWalletMaxMloki,omitempty"`
	JITMaxExpSecs        *int `json:"jitMaxExpSecs,omitempty"`
	// CircleMaxExpSecs/CircleFeesPpm/CirclePerWalletMaxMloki/CircleMinBudgetRenewal
	// are set only for circle_hub apps — the hub-wide defaults set at
	// creation time, for the same reason as above.
	CircleMaxExpSecs        *int    `json:"circleMaxExpSecs,omitempty"`
	CircleFeesPpm           *int    `json:"circleFeesPpm,omitempty"`
	CirclePerWalletMaxMloki *int    `json:"circlePerWalletMaxMloki,omitempty"`
	CircleMinBudgetRenewal  *string `json:"circleMinBudgetRenewal,omitempty"`
}

// CircleIdentitySummary is the bare identity, used for the circle-creation-time picker.
// UsedByCount is how many circle_hub apps currently reference this identity —
// the picker and the manage-identities list both need it to show which identities
// are safe to delete without a per-row round trip.
type CircleIdentitySummary struct {
	ID             uint   `json:"id"`
	Name           string `json:"name"`
	Policy         string `json:"policy"`
	ProviderPubkey string `json:"providerPubkey"`
	UsedByCount    int    `json:"usedByCount"`
}

// CircleIdentitySummaryWithCounts adds policy-specific counts, used when an
// identity is embedded inline on an App (list/get). FollowingCount is only
// populated from a non-blocking cache peek — nil means "not yet known," not
// "zero" — the frontend should render a loading state in that case rather
// than treating it as an authoritative zero.
type CircleIdentitySummaryWithCounts struct {
	CircleIdentitySummary
	FollowingCount *int `json:"followingCount,omitempty"`
	AllowlistCount int  `json:"allowlistCount"`
	// PolicySyncedAt is "last policy update" — when the current membership was
	// last confirmed from its source of truth: for "following", the relay
	// cache's last fetch time (nil if not yet cached, same semantics as
	// FollowingCount); for "allowlist", the last time a pubkey was added or
	// removed (nil if the allowlist has never been populated).
	PolicySyncedAt *time.Time `json:"policySyncedAt,omitempty"`
}

// CircleIdentityResponse is the full single-identity detail response —
// includes (for allowlist policy) the full pubkey list, unlike the inline
// per-App summary above which stays lean for list responses. UsedByCount is
// inherited from CircleIdentitySummary.
type CircleIdentityResponse struct {
	CircleIdentitySummaryWithCounts
	AllowlistPubkeys []string `json:"allowlistPubkeys,omitempty"`
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
	// JITPerWalletMaxMloki/JITMaxExpSecs update a jit_hub's JITHubConfig; nil
	// leaves the corresponding field unchanged. Ignored for other app kinds.
	JITPerWalletMaxMloki *int `json:"jitPerWalletMaxMloki"`
	JITMaxExpSecs        *int `json:"jitMaxExpSecs"`
	// CircleMaxExpSecs/CircleFeesPpm/CirclePerWalletMaxMloki/CircleMinBudgetRenewal
	// update a circle_hub's CircleHubConfig; nil leaves the
	// corresponding field unchanged. Ignored for other app kinds.
	CircleMaxExpSecs        *int    `json:"circleMaxExpSecs"`
	CircleFeesPpm           *int    `json:"circleFeesPpm"`
	CirclePerWalletMaxMloki *int    `json:"circlePerWalletMaxMloki"`
	CircleMinBudgetRenewal  *string `json:"circleMinBudgetRenewal"`
}

type TransferRequest struct {
	AmountLoki uint64 `json:"amountLoki"`
	FromAppId  *uint  `json:"fromAppId"`
	ToAppId    *uint  `json:"toAppId"`
}

type CreateAppRequest struct {
	Name                    string   `json:"name"`
	Pubkey                  string   `json:"pubkey"`
	MaxAmountLoki           uint64   `json:"maxAmount"`
	BudgetRenewal           string   `json:"budgetRenewal"`
	ExpiresAt               string   `json:"expiresAt"`
	Scopes                  []string `json:"scopes"`
	ReturnTo                string   `json:"returnTo"`
	Kind                    string   `json:"kind"`
	Metadata                Metadata `json:"metadata,omitempty"`
	UnlockPassword          string   `json:"unlockPassword"`
	JITPerWalletMaxMloki    int      `json:"jitPerWalletMaxMloki"`
	JITMaxExpSecs           int      `json:"jitMaxExpSecs"`
	CircleMaxExpSecs        int      `json:"circleMaxExpSecs"`
	CircleFeesPpm           int      `json:"circleFeesPpm"`
	CirclePerWalletMaxMloki int      `json:"circlePerWalletMaxMloki"`
	CircleMinBudgetRenewal  string   `json:"circleMinBudgetRenewal"`
	// CircleIdentityId reuses an existing CircleIdentity — when set, CirclePolicy/
	// CircleIdentityName/ProviderPubkey below are ignored.
	CircleIdentityId *uint `json:"circleIdentityId"`
	// CircleIdentityName/CirclePolicy/ProviderPubkey create a brand-new
	// CircleIdentity — used only when CircleIdentityId is nil.
	CircleIdentityName string `json:"circleIdentityName"`
	CirclePolicy       string `json:"circlePolicy"`
	ProviderPubkey     string `json:"providerPubkey"`
}

type CreateLightningAddressRequest struct {
	Address string `json:"address"`
	AppId   uint   `json:"appId"`
}

// JIT wallet / claim types.

// JITWalletRecipient describes one recipient's requested slice when creating
// a (possibly shared) JIT wallet — a wallet may serve several recipients at
// once, each with their own amount, all sharing the wallet's one expiry.
type JITWalletRecipient struct {
	IdentityType  string `json:"identity_type"` // "pubkey" | "connection_key"
	IdentityValue string `json:"identity_value"`
	IAPubkey      string `json:"ia_pubkey,omitempty"` // required iff identity_type == connection_key
	AmountMloki   int64  `json:"amount_mloki"`
}

type CreateJITWalletRequest struct {
	Recipients []JITWalletRecipient `json:"recipients"`
	ExpirySecs int                  `json:"expiry_secs,omitempty"` // shared by every recipient; 0 => hub's max
}

type CreateJITWalletResponse struct {
	AppID      uint                 `json:"app_id"`
	PairingURI string               `json:"pairing_uri"`
	ExpiresAt  int64                `json:"expires_at"`
	Recipients []JITWalletRecipient `json:"recipients"`
}

// ListJITWalletClaimsResponse is the paginated response for
// ListJITWalletClaims — mirrors ListTransactionsResponse's shape.
type ListJITWalletClaimsResponse struct {
	Claims     []JITWalletClaimResponse `json:"claims"`
	TotalCount uint64                   `json:"totalCount"`
	Counts     JITWalletClaimCounts     `json:"counts"`
}

// JIT claim status filter values, accepted by ListJITWalletClaims' status
// param and returned by jitClaimStatus.
const (
	JITAllocationStatusUnclaimed = "unclaimed"
	JITAllocationStatusClaimed   = "claimed"
	JITAllocationStatusExpired   = "expired"
)

// JITWalletClaimCounts totals a hub's claim rows by status, over the full
// unfiltered set — meant for a UI's per-tab counts.
type JITWalletClaimCounts struct {
	All       uint64 `json:"all"`
	Unclaimed uint64 `json:"unclaimed"`
	Claimed   uint64 `json:"claimed"`
	Expired   uint64 `json:"expired"`
}

// JITWalletClaimResponse represents one recipient's slice of a jit_wallet.
// ID is the claim's own row ID (delete via DeleteJITWalletClaim, unclaimed
// only); WalletAppID identifies the shared connection this slice belongs to
// (reveal via GetJITWalletConnection, delete the whole wallet via
// DeleteJITWallet). Claimed is a plain boolean (ClaimedAt != nil) — unlike
// the old single-recipient-per-wallet model, there's no spend-fraction
// derivation here, since claim_funds either pays a slice out completely or
// not at all.
type JITWalletClaimResponse struct {
	ID            uint   `json:"id"`
	WalletAppID   uint   `json:"wallet_app_id"`
	IdentityType  string `json:"identity_type"`
	IdentityValue string `json:"identity_value"`
	AmountMloki   int64  `json:"amount_mloki"`
	ExpiresAt     *int64 `json:"expires_at,omitempty"` // inherited from the wallet
	Claimed       bool   `json:"claimed"`
	ClaimedAt     *int64 `json:"claimed_at,omitempty"`
	CreatedAt     int64  `json:"created_at"`
}

type JITWalletConnectionResponse struct {
	PairingURI string `json:"pairing_uri"`
}

// Identity Authority registry types.

type AddIdentityAuthorityRequest struct {
	Pubkey    string   `json:"pubkey"`
	Name      string   `json:"name"`
	RelayURLs []string `json:"relay_urls,omitempty"`
}

type IdentityAuthorityResponse struct {
	Pubkey    string   `json:"pubkey"`
	Name      string   `json:"name"`
	RelayURLs []string `json:"relay_urls,omitempty"`
	CreatedAt int64    `json:"created_at"`
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
	FLNDAddress      string `json:"flndAddress"`
	FLNDCertFile     string `json:"flndCertFile"`
	FLNDMacaroonFile string `json:"flndMacaroonFile"`
	FLNDCertHex      string `json:"flndCertHex"`
	FLNDMacaroonHex  string `json:"flndMacaroonHex"`

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
	FLNDAddress           string            `json:"flndAddress"`
	FLNDCertHex           string            `json:"flndCertHex"`
	FLNDMacaroonHex       string            `json:"flndMacaroonHex"`
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

// CircleChildBalance describes one circle_wallet child's current isolated balance,
// used both for the wallets list and to warn an admin before deleting its circle_hub.
type CircleChildBalance struct {
	AppID uint   `json:"appId"`
	Name  string `json:"name"`
	// RequesterPubkey is the circle member's nostr identity (from
	// CircleWalletMembership), used by the frontend to resolve a profile
	// (avatar/display name) for the list — distinct from AppPubkey, which
	// is this wallet's own NWC connection pubkey.
	RequesterPubkey string `json:"requesterPubkey"`
	AppPubkey       string `json:"appPubkey"`
	BalanceMloki    int64  `json:"balanceMloki"`
}

// ListCircleChildrenBalancesResponse is the paginated response for
// ListCircleChildrenBalances — mirrors ListTransactionsResponse's shape.
type ListCircleChildrenBalancesResponse struct {
	Children   []CircleChildBalance `json:"children"`
	TotalCount uint64               `json:"totalCount"`
}

// CircleRefreshPreview reports the delta a following-policy refresh would
// apply — Added/Removed relative to the currently-stored allowlist — plus
// the full freshly-fetched list, so a confirmed refresh can act on exactly
// what was previewed without a second relay round-trip.
type CircleRefreshPreview struct {
	Pubkeys []string `json:"pubkeys"`
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

// DeleteCircleHubResult reports what DeleteCircleHub actually did:
// whether the provider itself was deleted, and which children were deleted vs
// left intact (skipped children only occur with CircleDeleteModeEmptyOnly when
// they still hold a nonzero balance — in that case HubDeleted is false).
type DeleteCircleHubResult struct {
	HubDeleted      bool   `json:"hubDeleted"`
	DeletedChildIDs []uint `json:"deletedChildIds"`
	SkippedChildIDs []uint `json:"skippedChildIds"`
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
	GeneralRelay                string              `json:"generalRelay"`
	SearchRelay                 string              `json:"searchRelay"`
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

// RedactForUnauthenticated strips fields that let an unauthenticated caller
// fingerprint a configured node (alias, relays, LSP host/pubkey, version,
// network, etc). It's a no-op during initial setup (SetupCompleted false),
// since the setup wizard reads these same fields before any credential
// exists. Callers must only invoke this once they've established the
// request is not authenticated.
func (r *InfoResponse) RedactForUnauthenticated() {
	if !r.SetupCompleted {
		return
	}
	r.BackendType = ""
	r.Version = ""
	r.Network = ""
	r.AutoUnlockPasswordSupported = false
	r.AutoUnlockPasswordEnabled = false
	r.Currency = ""
	r.FlokicoinDisplayFormat = ""
	r.Relays = []InfoResponseRelay{}
	r.Relay = ""
	r.GeneralRelay = ""
	r.SearchRelay = ""
	r.NodeAlias = ""
	r.MempoolUrl = ""
	r.LSPs = []LSPInfo{}
	r.LokihubServicesURL = ""
	r.SwapServiceUrl = ""
	r.MessageboardNwcUrl = ""
	r.EnableSwap = false
	r.EnableMessageboardNwc = false
	r.EnablePolling = false
}

type UpdateSettingsRequest struct {
	Currency               string            `json:"currency"`
	FlokicoinDisplayFormat string            `json:"flokicoinDisplayFormat"`
	LokihubServicesURL     string            `json:"lokihubServicesURL"`
	SwapServiceUrl         string            `json:"swapServiceUrl"`
	Relay                  string            `json:"relay"`
	GeneralRelay           *string           `json:"generalRelay"`
	SearchRelay            *string           `json:"searchRelay"`
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
	FeeSkim         uint64      `json:"feeSkim,omitempty"`
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
	AppId    *uint    `json:"appId"`
	Metadata Metadata `json:"metadata"`
}

type MakeOfferRequest struct {
	Description string `json:"description"`
}

type MakeInvoiceRequest struct {
	Amount                       uint64 `json:"amount"`
	Description                  string `json:"description"`
	DescriptionHash              string `json:"descriptionHash,omitempty"`
	AppId                        *uint  `json:"appId,omitempty"`
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
