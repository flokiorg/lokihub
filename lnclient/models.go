package lnclient

import (
	"context"
	"errors"
)

// TODO: remove JSON tags from these models (LNClient models should not be exposed directly)

type TLVRecord struct {
	Type uint64 `json:"type"`
	// hex-encoded value
	Value string `json:"value"`
}

type Metadata = map[string]interface{}

type NodeInfo struct {
	Alias       string
	Color       string
	Pubkey      string
	Network     string
	BlockHeight uint32
	BlockHash   string
}

// TODO: use uint for fields that cannot be negative
type Transaction struct {
	Type            string
	Invoice         string
	Description     string
	DescriptionHash string
	Preimage        string
	PaymentHash     string
	Amount          int64
	// AmountPaid is what the payer paid an invoice, 0 when not known.
	AmountPaid     int64
	FeesPaid       int64
	CreatedAt      int64
	ExpiresAt      *int64
	SettledAt      *int64
	Metadata       Metadata
	SettleDeadline *uint32 // block number for accepted hold invoices
}

type OnchainTransaction struct {
	AmountLoki       uint64 `json:"amountLoki"`
	CreatedAt        uint64 `json:"createdAt"`
	State            string `json:"state"`
	Type             string `json:"type"`
	NumConfirmations uint32 `json:"numConfirmations"`
	TxId             string `json:"txId"`
}

type NodeConnectionInfo struct {
	Pubkey  string `json:"pubkey"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// CustomMessage represents a custom protocol message received from or sent to a peer
type CustomMessage struct {
	PeerPubkey string
	Type       uint32
	Data       []byte
}

type LNClient interface {
	SendPaymentSync(payReq string, amount *uint64, feeLimitMloki *uint64) (*PayInvoiceResponse, error)
	SendKeysend(amount uint64, destination string, customRecords []TLVRecord, preimage string) (*PayKeysendResponse, error)
	GetPubkey() string
	GetInfo(ctx context.Context) (info *NodeInfo, err error)
	MakeInvoice(ctx context.Context, amount int64, description string, descriptionHash string, expiry int64, throughNodePubkey *string, lspJitChannelSCID *string, lspCltvExpiryDelta *uint16, lspFeeBaseMloki *uint64, lspFeeProportionalMillionths *uint32) (transaction *Transaction, err error)
	MakeHoldInvoice(ctx context.Context, amount int64, description string, descriptionHash string, expiry int64, paymentHash string) (transaction *Transaction, err error)
	SettleHoldInvoice(ctx context.Context, preimage string) (err error)
	CancelHoldInvoice(ctx context.Context, paymentHash string) (err error)
	LookupInvoice(ctx context.Context, paymentHash string) (transaction *Transaction, err error)
	ListTransactions(ctx context.Context, from, until, limit, offset uint64, unpaid bool, invoiceType string) (transactions []Transaction, err error)
	ListOnchainTransactions(ctx context.Context, from, until, limit, offset uint64) ([]OnchainTransaction, error)
	Shutdown() error
	ListChannels(ctx context.Context) (channels []Channel, err error)
	GetNodeConnectionInfo(ctx context.Context) (*NodeConnectionInfo, error)
	GetNodeStatus(ctx context.Context) (nodeStatus *NodeStatus, err error)
	ConnectPeer(ctx context.Context, connectPeerRequest *ConnectPeerRequest) error
	OpenChannel(ctx context.Context, openChannelRequest *OpenChannelRequest) (*OpenChannelResponse, error)
	CloseChannel(ctx context.Context, closeChannelRequest *CloseChannelRequest) (*CloseChannelResponse, error)
	UpdateChannel(ctx context.Context, updateChannelRequest *UpdateChannelRequest) error
	DisconnectPeer(ctx context.Context, peerId string) error

	GetNewOnchainAddress(ctx context.Context) (string, error)
	ResetRouter(key string) error
	GetOnchainBalance(ctx context.Context) (*OnchainBalanceResponse, error)
	GetBalances(ctx context.Context, includeInactiveChannels bool) (*BalancesResponse, error)
	RedeemOnchainFunds(ctx context.Context, toAddress string, amount uint64, feeRate *uint64, sendAll bool) (txId string, err error)
	// BroadcastTransaction publishes a fully signed raw transaction (hex-encoded)
	// to the network via the node's own wallet, rather than a third-party relay.
	BroadcastTransaction(ctx context.Context, txHex string) error
	SendPaymentProbes(ctx context.Context, invoice string) error
	SendSpontaneousPaymentProbes(ctx context.Context, amountMloki uint64, nodeId string) error
	ListPeers(ctx context.Context) ([]PeerDetails, error)
	GetLogOutput(ctx context.Context, maxLen int) ([]byte, error)
	SignMessage(ctx context.Context, message string) (string, error)
	// TransportSigner is deliberately NOT part of this interface — see its own
	// declaration below for why.
	GetStorageDir() (string, error)
	GetNetworkGraph(ctx context.Context, nodeIds []string) (NetworkGraphResponse, error)
	UpdateLastWalletSyncRequest()
	GetSupportedNIP47Methods() []string
	GetSupportedNIP47NotificationTypes() []string
	GetCustomNodeCommandDefinitions() []CustomNodeCommandDef
	ExecuteCustomNodeCommand(ctx context.Context, command *CustomNodeCommandRequest) (*CustomNodeCommandResponse, error)
	// Custom message support for LSPS protocols
	SendCustomMessage(ctx context.Context, peerPubkey string, msgType uint32, data []byte) error
	SubscribeCustomMessages(ctx context.Context) (<-chan CustomMessage, <-chan error, error)

	// Channel Acceptor
	SubscribeChannelAcceptor(ctx context.Context) (<-chan ChannelAcceptRequest, func(id string, accept bool, zeroConf bool) error, error)

	SetNodeAlias(ctx context.Context, alias string) error
}

// TransportSigner is the narrow capability the private-transport announcement
// needs: a BIP340 signature by the node's identity key.
//
// Kept OUT of LNClient on purpose. This repo has no mockery binary or
// .mockery.yaml, so its "generated" LNClient doubles are hand-maintained — there
// are eight of them, including five independent 42-method copies under lsps/ —
// and every method added to that interface has to be written into all eight.
// Since exactly one call site needs this, it is an optional interface the caller
// type-asserts instead, and nothing else has to change.
//
// SignSchnorrNodeKey signs sha256(msg), not msg. A caller holding a digest must
// pass its pre-image; for a Nostr event that is the canonical serialization,
// since an event id is sha256 of it.
type TransportSigner interface {
	SignSchnorrNodeKey(ctx context.Context, msg []byte) ([]byte, error)
}

// InvoiceStateKey is the Transaction.Metadata key carrying an invoice's state
// as the node reports it (OPEN, SETTLED, CANCELED, ACCEPTED), when it does.
const InvoiceStateKey = "invoice_state"

// OnchainOutput is one output of a transaction the node's wallet sees, paying
// one of its addresses. Confirmations is 0 for a transaction still in the
// mempool.
type OnchainOutput struct {
	TxID          string
	Vout          uint32
	Address       string
	AmountLoki    int64
	Confirmations uint32
	BlockHeight   int32
	// SeenAt is when the node's wallet first saw the transaction (unix
	// seconds), 0 when unknown.
	SeenAt int64
}

// OnchainOutputLister lists the outputs to the node's own addresses of the
// wallet transactions confirmed at fromHeight or above, and of every
// unconfirmed one; fromHeight 0 lists them all (NIP-CO §On-chain intake).
// Optional, like TransportSigner: a node without it sells no on-chain orders.
type OnchainOutputLister interface {
	ListOnchainOutputs(ctx context.Context, fromHeight int32) ([]OnchainOutput, error)
}

type ChannelAcceptRequest struct {
	ID         string
	NodePubkey string
	Capacity   uint64
}

type Channel struct {
	LocalBalance                             int64
	LocalSpendableBalance                    int64
	RemoteBalance                            int64
	Id                                       string
	RemotePubkey                             string
	FundingTxId                              string
	FundingTxVout                            uint32
	Active                                   bool
	Public                                   bool
	InternalChannel                          interface{}
	Confirmations                            *uint32
	ConfirmationsRequired                    *uint32
	ForwardingFeeBaseMloki                   uint32
	ForwardingFeeProportionalMillionths      uint32
	UnspendablePunishmentReserve             uint64
	CounterpartyUnspendablePunishmentReserve uint64
	Error                                    *string
	IsOutbound                               bool
}

type NodeStatus struct {
	IsReady            bool        `json:"isReady"`
	InternalNodeStatus interface{} `json:"internalNodeStatus"`
}

type ConnectPeerRequest struct {
	Pubkey  string `json:"pubkey"`
	Address string `json:"address"`
	Port    uint16 `json:"port"`
}

type OpenChannelRequest struct {
	Pubkey     string `json:"pubkey"`
	AmountLoki int64  `json:"amountLoki"`
	Public     bool   `json:"public"`
}

type OpenChannelResponse struct {
	FundingTxId string `json:"fundingTxId"`
}

type CloseChannelRequest struct {
	ChannelId string `json:"channelId"`
	NodeId    string `json:"nodeId"`
	Force     bool   `json:"force"`
}

type UpdateChannelRequest struct {
	ChannelId                                string `json:"channelId"`
	NodeId                                   string `json:"nodeId"`
	ForwardingFeeBaseMloki                   uint32 `json:"forwardingFeeBaseMloki"`
	ForwardingFeeProportionalMillionths      uint32 `json:"forwardingFeeProportionalMillionths"`
	MaxDustHtlcExposureFromFeeRateMultiplier uint64 `json:"maxDustHtlcExposureFromFeeRateMultiplier"`
}

type CloseChannelResponse struct {
}

type PendingBalanceDetails struct {
	ChannelId     string `json:"channelId"`
	NodeId        string `json:"nodeId"`
	Amount        uint64 `json:"amount"`
	FundingTxId   string `json:"fundingTxId"`
	FundingTxVout uint32 `json:"fundingTxVout"`
}

type OnchainBalanceResponse struct {
	Spendable                          int64                   `json:"spendable"`
	Total                              int64                   `json:"total"`
	Reserved                           int64                   `json:"reserved"`
	PendingBalancesFromChannelClosures uint64                  `json:"pendingBalancesFromChannelClosures"`
	PendingBalancesDetails             []PendingBalanceDetails `json:"pendingBalancesDetails"`
	PendingSweepBalancesDetails        []PendingBalanceDetails `json:"pendingSweepBalancesDetails"`
	InternalBalances                   interface{}             `json:"internalBalances"`
}

type PeerDetails struct {
	NodeId      string `json:"nodeId"`
	Address     string `json:"address"`
	IsPersisted bool   `json:"isPersisted"`
	IsConnected bool   `json:"isConnected"`
}
type LightningBalanceResponse struct {
	TotalSpendable       int64 `json:"totalSpendable"`
	TotalReceivable      int64 `json:"totalReceivable"`
	NextMaxSpendable     int64 `json:"nextMaxSpendable"`
	NextMaxReceivable    int64 `json:"nextMaxReceivable"`
	NextMaxSpendableMPP  int64 `json:"nextMaxSpendableMPP"`
	NextMaxReceivableMPP int64 `json:"nextMaxReceivableMPP"`
}

type PayInvoiceResponse struct {
	Preimage string `json:"preimage"`
	Fee      uint64 `json:"fee"`
}

type PayOfferResponse = struct {
	Preimage    string `json:"preimage"`
	Fee         uint64 `json:"fee"`
	PaymentHash string `json:"payment_hash"`
}

type PayKeysendResponse struct {
	Fee uint64 `json:"fee"`
}

type BalancesResponse struct {
	Onchain   OnchainBalanceResponse   `json:"onchain"`
	Lightning LightningBalanceResponse `json:"lightning"`
}

type NetworkGraphResponse = interface{}

type PaymentFailedEventProperties struct {
	Transaction *Transaction
	Reason      string
}

type PaymentForwardedEventProperties struct {
	TotalFeeEarnedMloki          uint64
	OutboundAmountForwardedMloki uint64
}

type CustomNodeCommandArgDef struct {
	Name        string
	Description string
}

type CustomNodeCommandDef struct {
	Name        string
	Description string
	Args        []CustomNodeCommandArgDef
}

type CustomNodeCommandArg struct {
	Name  string
	Value string
}

type CustomNodeCommandRequest struct {
	Name string
	Args []CustomNodeCommandArg
}

type CustomNodeCommandResponse struct {
	Response interface{}
}

func NewCustomNodeCommandResponseEmpty() *CustomNodeCommandResponse {
	return &CustomNodeCommandResponse{
		Response: struct{}{},
	}
}

var ErrUnknownCustomNodeCommand = errors.New("unknown custom node command")

// PaymentStatus is what a backend knows about an outgoing payment's outcome.
type PaymentStatus string

const (
	PaymentStatusSucceeded PaymentStatus = "succeeded"
	// PaymentStatusFailed means the payment definitely did not complete:
	// it failed, or the node never started it.
	PaymentStatusFailed PaymentStatus = "failed"
	// PaymentStatusInFlight means the outcome is not known yet.
	PaymentStatusInFlight PaymentStatus = "in_flight"
)

// PaymentLookup is one outgoing payment's state.
type PaymentLookup struct {
	Status        PaymentStatus
	Preimage      string
	FeeMloki      uint64
	FailureReason string
}

// PaymentLooker is implemented by backends that can report an outgoing
// payment's state by its hash. Kept out of LNClient for the same reason as
// TransportSigner: adding a method there means updating every hand-written
// LNClient double. Callers type-assert and treat a backend without it as
// unable to say.
type PaymentLooker interface {
	LookupPayment(ctx context.Context, paymentHash string) (*PaymentLookup, error)
}

// ErrPaymentPending is returned (wrapped) by SendPaymentSync when the payment
// reached the node but its outcome could not be learned, so it may still
// settle. Callers must not treat it as a failure.
var ErrPaymentPending = errors.New("payment outcome unknown, it may still be in flight")

// default invoice expiry in seconds (1 day)
const DEFAULT_INVOICE_EXPIRY = 86400

// ErrInvoiceNotFound: the node has no invoice for the payment hash, so there is
// nothing left to cancel.
var ErrInvoiceNotFound = errors.New("the node has no such invoice")

type holdInvoiceCanceledError struct {
}

func NewHoldInvoiceCanceledError() error {
	return &holdInvoiceCanceledError{}
}

func (err *holdInvoiceCanceledError) Error() string {
	return "Hold invoice canceled"
}
