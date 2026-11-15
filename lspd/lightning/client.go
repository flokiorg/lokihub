package lightning

import (
	"context"
	"time"

	"github.com/flokiorg/go-flokicoin/wire"
)

type GetInfoResult struct {
	Alias               string
	Pubkey              string
	Version             string
	BlockHeight         uint32
	SyncedToChain       bool
	SyncedToGraph       bool
	NumActiveChannels   uint32
	NumPendingChannels  uint32
	NumInactiveChannels uint32
	NumPeers            uint32
	// Chain and Network are the node's chain name (e.g. "flokicoin") and
	// network (mainnet, testnet, signet, regtest, ...) from GetInfo's
	// chains; empty when the node reports none.
	Chain   string
	Network string
}

type Peer struct {
	Pubkey        string `json:"pub_key"`
	Address       string `json:"address"`
	Inbound       bool   `json:"inbound"`
	BytesSent     uint64 `json:"bytes_sent"`
	BytesReceived uint64 `json:"bytes_received"`
	LokiSent      uint64 `json:"loki_sent"`
	LokiReceived  uint64 `json:"loki_received"`
	Active        bool   `json:"active"`
	Alias         string `json:"alias"`
	Color         string `json:"color"`
}

type NodeInfo struct {
	Pubkey string
	Alias  string
	Color  string
}

type GetChannelResult struct {
	AliasScid        *ShortChannelID
	ConfirmedScid    *ShortChannelID
	HtlcMinimumMloki uint64
}

type RoutingPolicy struct {
	Ppm           uint32
	BaseMloki     uint64
	TimeLockDelta uint32
}

type OpenChannelRequest struct {
	Destination     []byte
	CapacityLoki    uint64
	MinConfs        *uint32
	FeeLokiPerVByte *float64
	TargetConf      *uint32
	RoutingPolicy   *RoutingPolicy
	ZeroConf        bool
	PushLoki        uint64
	AnnounceChannel bool
}

type HTLC struct {
	Incoming         bool
	AmountMloki      uint64
	HashLock         []byte
	ExpirationHeight uint32
	HtlcIndex        uint64
}

type Channel struct {
	AliasScid     *ShortChannelID
	ConfirmedScid *ShortChannelID
	ChannelPoint  *wire.OutPoint
	PeerId        []byte
	PendingHtlcs  []HTLC
	Active        bool
	Capacity      int64
	LocalBalance  int64
	RemoteBalance int64
}

type Client interface {
	GetInfo() (*GetInfoResult, error)
	IsConnected(destination []byte) (bool, error)
	OpenChannel(req *OpenChannelRequest) (*wire.OutPoint, error)
	// UpdateChannelPolicy sets policy (fees + CLTV delta) on an
	// already-open channel. This is deliberately separate from
	// OpenChannel: OpenChannelSync has no CLTV-delta field, but applying
	// it via a follow-up call *immediately* after OpenChannelSync returns
	// -- before the channel's own link is registered -- silently
	// no-ops the CLTV delta specifically, even though the RPC reports no
	// error and the fee fields do take effect (confirmed live
	// 2026-07-16: the channel stayed stuck at flnd's own devnet-wide
	// default of 800 blocks instead of the requested 40, which by itself
	// was enough to make every forwarding attempt fail with
	// hop.FinalHtlcInvalidCltv -- initially indistinguishable from the
	// unrelated payer-side pathfinding issues chased across earlier
	// sessions, see BACKLOG.md). Callers must wait for the channel to be
	// confirmed active (WaitChannelPointActive) before calling this.
	UpdateChannelPolicy(channelPoint wire.OutPoint, policy *RoutingPolicy) error
	GetChannel(peerID []byte, channelPoint wire.OutPoint) (*GetChannelResult, error)
	WaitChannelPointActive(peerID []byte, channelPoint wire.OutPoint, deadline time.Time) (*GetChannelResult, error)
	GetPeerId(scid *ShortChannelID) ([]byte, error)
	GetClosedChannels(nodeID string, channelPoints map[string]uint64) (map[string]uint64, error)
	WaitOnline(peerID []byte, deadline time.Time) error
	WaitChannelActive(peerID []byte, deadline time.Time) error
	ListChannels() ([]*Channel, error)
	ListPeers() ([]*Peer, error)
	SubscribeInvoices() (<-chan *InvoiceUpdate, <-chan error, error)
	// SubscribeSingleInvoice streams state transitions for one invoice,
	// identified by its payment hash (H-12): unlike SubscribeInvoices
	// (which only ever delivers "newly added" and "settled" -- confirmed
	// live against real flnd 2026-07-17, see settlement_service.go's
	// heldInvoiceSafetyNetPollInterval doc comment), this per-invoice
	// stream does deliver ACCEPTED, giving near-instant hold-invoice
	// detection instead of relying on a poll. Takes ctx explicitly (unlike
	// SubscribeInvoices, which hardcodes context.Background()) so the
	// stream can be torn down on shutdown via context cancellation.
	SubscribeSingleInvoice(ctx context.Context, paymentHash []byte) (<-chan *InvoiceUpdate, <-chan error, error)
	LookupInvoiceState(ctx context.Context, paymentRequest string) (InvoiceState, error)
	// LookupInvoiceStateByHash is LookupInvoiceState's cheaper sibling
	// (H-11): callers that already have the payment hash (e.g. derived
	// locally from a hold invoice's stored preimage) can skip the
	// DecodePayReq round-trip LookupInvoiceState needs to derive it from a
	// bolt11 string.
	LookupInvoiceStateByHash(ctx context.Context, paymentHash []byte) (InvoiceState, error)
	// SettleInvoice releases a held (ACCEPTED) hold invoice's HTLCs by
	// revealing preimage, the point of no return for a hold-invoice
	// payment: call only once the LSP is actually committed (e.g. the
	// funding tx for the channel it was paying for has been broadcast).
	SettleInvoice(ctx context.Context, preimage []byte) error
	// CancelInvoice releases a held (ACCEPTED) hold invoice's HTLCs back
	// to the payer without settling -- the instant-refund path: the
	// payer's HTLC fails back through the route immediately, no on-chain
	// transaction required. Safe to call even if the invoice was never
	// accepted (cancelling an OPEN or already-CANCELED invoice succeeds).
	CancelInvoice(ctx context.Context, paymentHash []byte) error
	WalletBalance(ctx context.Context) (*WalletBalanceResult, error)

	GetBlockHeight() (uint32, error)
	QueryRoutes(destination []byte, amountMloki uint64, outgoingChanId uint64) ([]*Route, error)
	// IsChannelRoutable reports whether scid has both directional policies
	// present in the graph -- the same signal upstream LND's own zero-conf
	// itest coverage checks for (DescribeGraph), which reads straight from
	// the graph DB. Prefer this over QueryRoutes for a "is this freshly
	// opened channel ready yet" check: QueryRoutes' pathfinding reads from
	// an in-memory graph cache that can lag well behind the DB for a
	// locally-added zero-conf edge, even though the DB itself is correct
	// within the same second the channel activates.
	IsChannelRoutable(scid uint64) (bool, error)
	// IsChannelPolicyApplied reports whether the local node's own
	// advertised policy for scid already shows timeLockDelta. Needed
	// because UpdateChannelPolicy's own success return doesn't guarantee
	// this yet -- see its doc comment for why. peerPubkey identifies
	// which side of the channel is the remote peer, so the other side
	// can be checked as "ours".
	IsChannelPolicyApplied(scid uint64, peerPubkey []byte, timeLockDelta uint32) (bool, error)
	GetNodeInfo(pubkey string) (*NodeInfo, error)
}

type WalletBalanceResult struct {
	TotalBalance       int64
	ConfirmedBalance   int64
	UnconfirmedBalance int64
}

// InvoiceState represents the state of an invoice
type InvoiceState int

const (
	InvoiceStateOpen InvoiceState = iota
	InvoiceStateSettled
	InvoiceStateCanceled
	InvoiceStateAccepted
)

// InvoiceUpdate represents an invoice update event from LND
type InvoiceUpdate struct {
	State          InvoiceState
	PaymentRequest string
	AmountMloki    uint64
	SettleDate     time.Time
}

type Route struct {
	TotalTimeLock uint32
	TotalFees     int64
	TotalAmt      int64
	Hops          []*Hop
}

type Hop struct {
	ChanId           uint64
	ChanCapacity     int64
	AmtToForward     int64
	Fee              int64
	Expiry           uint32
	AmtToForwardMsat int64
	FeeMsat          int64
	PubKey           string
	CustomRecords    map[uint64][]byte
}
