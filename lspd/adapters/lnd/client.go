package lnd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/chainrpc"
	"github.com/flokiorg/flnd/lnrpc/invoicesrpc"
	"github.com/flokiorg/flnd/lnrpc/routerrpc"
	"github.com/flokiorg/flnd/lnrpc/signrpc"
	"github.com/flokiorg/go-flokicoin/chaincfg/chainhash"
	"github.com/flokiorg/go-flokicoin/wire"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/platform/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type LndClient struct {
	client              lnrpc.LightningClient
	routerClient        routerrpc.RouterClient
	invoicesClient      invoicesrpc.InvoicesClient
	chainNotifierClient chainrpc.ChainNotifierClient
	signerClient        signrpc.SignerClient
	conn                *grpc.ClientConn
	listenerCtx         context.Context
	listenerCancel      context.CancelFunc
	peersubs            map[string]map[uint64]chan struct{}
	chansubs            map[string]map[uint64]chan struct{}
	submtx              sync.RWMutex
	index               uint64

	// peerCacheMtx guards scidToPeer/connectedPeers (P-2): GetPeerId and
	// IsConnected are called on every intercepted HTLC, so they read from
	// this in-memory cache instead of doing a full ListChannels/ListPeers
	// scan inline on that path. The cache is kept warm by
	// refreshPeerCache, called on every channel/peer event
	// (listenChannelEvents/listenPeerEvents) and on a periodic safety-net
	// timer -- see refreshPeerCache's own doc comment for why a full
	// resync per event, not incremental patching, was the deliberate
	// choice here.
	peerCacheMtx   sync.RWMutex
	scidToPeer     map[uint64][]byte
	connectedPeers map[string]bool
}

func NewLndClient(conf *config.LndConfig) (*LndClient, error) {
	macaroon, err := conf.Macaroon.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolving flnd.macaroon: %w", err)
	}
	_, err = hex.DecodeString(macaroon)
	if err != nil {
		return nil, fmt.Errorf("failed to decode macaroon: %w", err)
	}

	cert, err := conf.Cert.Resolve()
	if err != nil {
		return nil, fmt.Errorf("resolving flnd.cert: %w", err)
	}
	// Creds file to connect to LND gRPC
	// User requested specific format: Hex-encoded PEM string.
	// We hex decode the string, then parse it as a PEM certificate.
	certBytes, err := hex.DecodeString(cert)
	if err != nil {
		return nil, fmt.Errorf("failed to decode certificate (expecting hex-encoded PEM): %w", err)
	}

	cp := x509.NewCertPool()
	if !cp.AppendCertsFromPEM(certBytes) {
		return nil, fmt.Errorf("failed to append certificates from PEM (check if hex decodes to valid PEM)")
	}

	// We use empty server name override because we authorize the specific cert in the pool.
	// However, if the cert doesn't have the IP/SAN of the LND host, we might need an override.
	// But usually LND certs have them.
	var creds credentials.TransportCredentials
	if conf.DisableTls {
		// Create a TLS config that skips verification
		tlsConfig := &tls.Config{
			InsecureSkipVerify: true,
		}
		creds = credentials.NewTLS(tlsConfig)
		log.Warn().Msg("LND TLS verification DISABLED via configuration. This is unsafe for production if traffic traverses untrusted networks.")
	} else {
		creds = credentials.NewClientTLSFromCert(cp, "")
	}
	macCred := NewMacaroonCredential(macaroon)

	// Address of an LND instance
	conn, err := grpc.Dial(
		conf.Address,
		grpc.WithTransportCredentials(creds),
		grpc.WithPerRPCCredentials(macCred),
	)
	if err != nil {
		log.Fatal().Msgf("Failed to connect to LND gRPC: %v", err)
	}

	client := lnrpc.NewLightningClient(conn)
	routerClient := routerrpc.NewRouterClient(conn)
	invoicesClient := invoicesrpc.NewInvoicesClient(conn)
	chainNotifierClient := chainrpc.NewChainNotifierClient(conn)
	signerClient := signrpc.NewSignerClient(conn)
	return &LndClient{
		client:              client,
		routerClient:        routerClient,
		invoicesClient:      invoicesClient,
		chainNotifierClient: chainNotifierClient,
		signerClient:        signerClient,
		conn:                conn,
		peersubs:            make(map[string]map[uint64]chan struct{}),
		chansubs:            make(map[string]map[uint64]chan struct{}),
		scidToPeer:          make(map[uint64][]byte),
		connectedPeers:      make(map[string]bool),
	}, nil
}

// RawClient exposes the underlying lnrpc.LightningClient for callers that
// need gRPC methods the lightning.Client port deliberately doesn't
// abstract (e.g. AddInvoice, ConnectPeer, custom messages) — flspd itself
// never calls these as the LSP, but the integration-test scenario runner
// needs them to drive the client/payer roles it isn't the LSP for.
func (c *LndClient) RawClient() lnrpc.LightningClient {
	return c.client
}

// RouterClient exposes the underlying routerrpc.RouterClient for the same
// reason as RawClient — e.g. SendPaymentV2, which the integration-test
// scenario runner needs to actually pay an invoice from a non-LSP role.
func (c *LndClient) RouterClient() routerrpc.RouterClient {
	return c.routerClient
}

// InvoicesClient exposes the raw invoicesrpc client for the same reason as
// RouterClient -- the H-12 devnet verification test needs to drive
// SubscribeSingleInvoice/AddHoldInvoice directly against real flnd,
// independent of flspd's own SubscribeSingleInvoice wrapper, to prove the
// underlying RPC actually behaves as documented rather than trusting it.
func (c *LndClient) InvoicesClient() invoicesrpc.InvoicesClient {
	return c.invoicesClient
}

func (c *LndClient) Close() {
	cancel := c.listenerCancel
	if cancel != nil {
		cancel()
	}
	c.conn.Close()
}

func (c *LndClient) StartListeners() {
	c.listenerCtx, c.listenerCancel = context.WithCancel(context.Background())
	// Seed the cache synchronously so GetPeerId/IsConnected never race a
	// caller against the first background refresh -- by the time
	// StartListeners returns, the cache reflects real state (or, on a
	// transient RPC failure here, self-heals via the periodic ticker
	// below rather than staying empty forever).
	c.refreshPeerCache()
	go c.listenPeerEvents()
	go c.listenChannelEvents()
	go c.periodicPeerCacheRefresh()
}

// periodicPeerCacheRefresh is the safety net under refreshPeerCache's
// event-triggered calls: if a channel/peer event's implications for
// scidToPeer turn out to be less complete than assumed (see
// refreshPeerCache's doc comment), or a subscription gap loses events
// despite the reconnect-triggered refresh, staleness is bounded to this
// interval rather than persisting indefinitely.
func (c *LndClient) periodicPeerCacheRefresh() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.listenerCtx.Done():
			return
		case <-ticker.C:
			c.refreshPeerCache()
		}
	}
}

// refreshPeerCache rebuilds scidToPeer and connectedPeers from a fresh
// ListChannels/ListPeers snapshot (P-2). A full resync on every
// channel/peer event, rather than patching the two maps incrementally
// from each event's own payload, is the deliberate choice: OPEN_CHANNEL/
// CLOSED_CHANNEL events carry a complete *Channel/*ChannelCloseSummary
// (so incremental add/remove would be precise there), but ACTIVE_CHANNEL
// only carries a bare ChannelPoint, and it's genuinely unclear from the
// proto alone whether a zero-conf channel's alias-to-confirmed SCID
// transition (which GetPeerId must resolve correctly, since a payer may
// route using either) always fires its own event or requires a fresh
// ListChannels read to observe. This runs in the listener goroutine, not
// on the interception path, so the extra RPC cost of "always resync"
// instead of "resync only when provably necessary" is free where it
// actually matters -- getting a misrouted-HTLC edge case wrong here is
// not a tradeoff worth making for it.
func (c *LndClient) refreshPeerCache() {
	ctx := context.Background()

	chans, err := c.client.ListChannels(ctx, &lnrpc.ListChannelsRequest{})
	if err != nil {
		log.Error().Err(err).Msg("LND: refreshPeerCache: ListChannels failed")
	} else {
		scidToPeer := make(map[uint64][]byte, len(chans.Channels))
		for _, ch := range chans.Channels {
			peerBytes, err := hex.DecodeString(ch.RemotePubkey)
			if err != nil {
				log.Warn().Err(err).Str("remote_pubkey", ch.RemotePubkey).Msg("LND: refreshPeerCache: undecodable remote pubkey, skipping channel")
				continue
			}
			if ch.ChanId != 0 {
				scidToPeer[ch.ChanId] = peerBytes
			}
			if ch.ZeroConfConfirmedScid != 0 {
				scidToPeer[ch.ZeroConfConfirmedScid] = peerBytes
			}
			for _, alias := range ch.AliasScids {
				scidToPeer[alias] = peerBytes
			}
		}
		c.peerCacheMtx.Lock()
		c.scidToPeer = scidToPeer
		c.peerCacheMtx.Unlock()
	}

	peers, err := c.client.ListPeers(ctx, &lnrpc.ListPeersRequest{})
	if err != nil {
		log.Error().Err(err).Msg("LND: refreshPeerCache: ListPeers failed")
		return
	}
	connectedPeers := make(map[string]bool, len(peers.Peers))
	for _, p := range peers.Peers {
		connectedPeers[p.PubKey] = true
	}
	c.peerCacheMtx.Lock()
	c.connectedPeers = connectedPeers
	c.peerCacheMtx.Unlock()
}

func (c *LndClient) listenPeerEvents() {
	ctx := c.listenerCtx
	for {
		if ctx.Err() != nil {
			return
		}

		sub, err := c.client.SubscribePeerEvents(
			ctx,
			&lnrpc.PeerEventSubscription{},
		)
		if err != nil {
			log.Debug().Err(err).Msg("LND: SubscribePeerEvents failed, retrying")
			<-time.After(time.Second)
			continue
		}

		// A fresh (re)subscribe may have missed events during the gap
		// since the last one (initial startup, or a dropped stream) --
		// resync now rather than waiting for the periodic ticker.
		c.refreshPeerCache()

		for {
			if ctx.Err() != nil {
				return
			}

			msg, err := sub.Recv()
			if err != nil {
				status, ok := status.FromError(err)
				if ok && status.Code() == codes.Canceled {
					log.Debug().Msg("LND: Peer event listener stopped - context canceled")
					break
				}

				log.Error().Err(err).Msg("LND: Peer event listener error")
				break
			}

			// Both ONLINE and OFFLINE affect connectedPeers (P-2); only
			// ONLINE wakes WaitOnline's own subscribers, unchanged from
			// before.
			c.refreshPeerCache()

			if msg.Type != lnrpc.PeerEvent_PEER_ONLINE {
				continue
			}

			c.submtx.RLock()
			subs, ok := c.peersubs[msg.PubKey]
			if ok {
				for _, sub := range subs {
					sub <- struct{}{}
				}
			}
			c.submtx.RUnlock()
		}

		<-time.After(time.Second)
	}
}

func (c *LndClient) listenChannelEvents() {
	ctx := c.listenerCtx
	for {
		if ctx.Err() != nil {
			return
		}

		sub, err := c.client.SubscribeChannelEvents(
			ctx,
			&lnrpc.ChannelEventSubscription{},
		)
		if err != nil {
			log.Debug().Err(err).Msg("LND: SubscribeChannelEvents failed, retrying")
			<-time.After(time.Second)
			continue
		}

		// A fresh (re)subscribe may have missed events during the gap
		// since the last one (initial startup, or a dropped stream) --
		// resync now rather than waiting for the periodic ticker.
		c.refreshPeerCache()

		for {
			if ctx.Err() != nil {
				return
			}

			msg, err := sub.Recv()
			if err != nil {
				status, ok := status.FromError(err)
				if ok && status.Code() == codes.Canceled {
					log.Debug().Msg("LND: Channel event listener stopped - context canceled")
					break
				}

				log.Error().Err(err).Msg("LND: Channel event listener error")
				break
			}

			// Any channel event (open/closed/active/inactive/...) can
			// affect scidToPeer -- resync on all of them (P-2's own doc
			// comment on refreshPeerCache explains why full resync, not
			// per-type incremental patching). The chansubs signaling
			// below stays scoped to ACTIVE_CHANNEL only, unchanged.
			c.refreshPeerCache()

			if msg.Type != lnrpc.ChannelEventUpdate_ACTIVE_CHANNEL {
				continue
			}

			ch := msg.GetActiveChannel()
			point, err := extractChannelPoint(ch)
			if err != nil {
				log.Debug().Err(err).Interface("channel", ch).Msg("LND: Failed to extract channel point")
				continue
			}

			c.submtx.RLock()
			subs, ok := c.chansubs[point]
			if ok {
				for _, sub := range subs {
					sub <- struct{}{}
				}
			}
			c.submtx.RUnlock()
		}

		<-time.After(time.Second)
	}
}

func extractChannelPoint(cp *lnrpc.ChannelPoint) (string, error) {
	str := cp.GetFundingTxidStr()
	if str == "" {
		b := cp.GetFundingTxidBytes()
		h, err := chainhash.NewHash(b)
		if err != nil {
			return "", err
		}
		str = h.String()
	}

	return fmt.Sprintf("%s:%d", str, cp.OutputIndex), nil
}

func (c *LndClient) GetInfo() (*lightning.GetInfoResult, error) {
	info, err := c.client.GetInfo(context.Background(), &lnrpc.GetInfoRequest{})
	if err != nil {
		log.Error().Msgf("LND: client.GetInfo() error: %v", err)
		return nil, err
	}

	var chain, network string
	if len(info.Chains) > 0 && info.Chains[0] != nil {
		chain = info.Chains[0].Chain //nolint:staticcheck // SA1019: lnrpc has no replacement field and flnd still fills it
		network = info.Chains[0].Network
	}

	return &lightning.GetInfoResult{
		Chain:               chain,
		Network:             network,
		Alias:               info.Alias,
		Pubkey:              info.IdentityPubkey,
		Version:             info.Version,
		BlockHeight:         info.BlockHeight,
		SyncedToChain:       info.SyncedToChain,
		SyncedToGraph:       info.SyncedToGraph,
		NumActiveChannels:   info.NumActiveChannels,
		NumPendingChannels:  info.NumPendingChannels,
		NumInactiveChannels: info.NumInactiveChannels,
		NumPeers:            info.NumPeers,
	}, nil
}

// IsConnected reads from the cache refreshPeerCache maintains (P-2)
// instead of calling ListPeers inline -- this is called on every
// intercepted HTLC.
func (c *LndClient) IsConnected(destination []byte) (bool, error) {
	pubkey := hex.EncodeToString(destination)

	c.peerCacheMtx.RLock()
	connected := c.connectedPeers[pubkey]
	c.peerCacheMtx.RUnlock()

	if connected {
		log.Debug().Str("pubkey", pubkey).Msg("LND: Destination peer is online")
	} else {
		log.Debug().Str("pubkey", pubkey).Msg("LND: Destination peer is offline")
	}
	return connected, nil
}

func (c *LndClient) OpenChannel(req *lightning.OpenChannelRequest) (*wire.OutPoint, error) {
	var baseFee uint64
	var feeRate uint64
	useFees := false
	if req.RoutingPolicy != nil {
		useFees = true
		baseFee = req.RoutingPolicy.BaseMloki
		feeRate = uint64(req.RoutingPolicy.Ppm)
	}
	lnReq := &lnrpc.OpenChannelRequest{
		NodePubkey:         req.Destination,
		LocalFundingAmount: int64(req.CapacityLoki),
		PushSat:            int64(req.PushLoki),
		Private:            !req.AnnounceChannel,
		CommitmentType:     lnrpc.CommitmentType_ANCHORS,
		BaseFee:            baseFee,
		UseBaseFee:         useFees,
		FeeRate:            feeRate,
		UseFeeRate:         useFees,
	}

	if req.ZeroConf {
		lnReq.ZeroConf = true
		lnReq.SpendUnconfirmed = true
	}

	if req.MinConfs != nil {
		minConfs := *req.MinConfs
		lnReq.MinConfs = int32(minConfs)
		if minConfs == 0 {
			lnReq.SpendUnconfirmed = true
		}
	}

	if req.FeeLokiPerVByte != nil && *req.FeeLokiPerVByte > 0 {
		lnReq.SatPerVbyte = uint64(math.Ceil(*req.FeeLokiPerVByte))
	} else if req.TargetConf != nil {
		lnReq.TargetConf = int32(*req.TargetConf)
	} else {
		// Default to 6 confirmations if nothing is specified to avoid LND warning
		lnReq.TargetConf = 6
	}

	channelPoint, err := c.client.OpenChannelSync(context.Background(), lnReq)
	if err != nil {
		log.Error().Msgf("LND: client.OpenChannelSync(%x, %v) error: %v", req.Destination, req.CapacityLoki, err)
		return nil, fmt.Errorf("LND: OpenChannel() error: %w", err)
	}

	result, err := lightning.NewOutPoint(channelPoint.GetFundingTxidBytes(), channelPoint.OutputIndex)
	if err != nil {
		log.Error().Msgf("LND: OpenChannel returned invalid outpoint. error: %v", err)
		return nil, err
	}

	// TimeLockDelta (and any other routing policy) is applied separately,
	// once the channel is confirmed active -- see UpdateChannelPolicy's
	// doc comment on lightning.Client for why doing it here, immediately
	// after OpenChannelSync returns, doesn't reliably take effect.

	return result, nil
}

func (c *LndClient) UpdateChannelPolicy(channelPoint wire.OutPoint, policy *lightning.RoutingPolicy) error {
	if policy == nil {
		return nil
	}

	_, err := c.client.UpdateChannelPolicy(context.Background(), &lnrpc.PolicyUpdateRequest{
		Scope: &lnrpc.PolicyUpdateRequest_ChanPoint{
			ChanPoint: &lnrpc.ChannelPoint{
				FundingTxid: &lnrpc.ChannelPoint_FundingTxidBytes{
					FundingTxidBytes: channelPoint.Hash[:],
				},
				OutputIndex: channelPoint.Index,
			},
		},
		BaseFeeMsat:   int64(policy.BaseMloki),
		FeeRate:       float64(policy.Ppm) / 1_000_000,
		TimeLockDelta: policy.TimeLockDelta,
	})
	return err
}

func (c *LndClient) GetChannel(peerID []byte, channelPoint wire.OutPoint) (*lightning.GetChannelResult, error) {
	r, err := c.client.ListChannels(context.Background(), &lnrpc.ListChannelsRequest{Peer: peerID})
	if err != nil {
		log.Error().Msgf("client.ListChannels(%x) error: %v", peerID, err)
		return nil, err
	}

	channelPointStr := channelPoint.String()

	for _, c := range r.Channels {
		log.Trace().Msgf("LND: getChannel(%x) found channel %v", peerID, c.ChanId)
		if c.ChannelPoint == channelPointStr && c.Active {
			aliasScid, confirmedScid := mapScidsFromChannel(c)
			return &lightning.GetChannelResult{
				AliasScid:        aliasScid,
				ConfirmedScid:    confirmedScid,
				HtlcMinimumMloki: c.LocalConstraints.MinHtlcMsat,
			}, nil
		}
	}
	log.Info().Msgf("No channel found: getChannel(%x)", peerID)
	return nil, fmt.Errorf("no channel found")
}

// WaitChannelPointActive blocks until the specific channel at
// channelPoint becomes active, then returns its scid info -- driven by
// the same channel-event subscription listenChannelEvents already
// maintains (chansubs, keyed by channel point string, already used by
// WaitChannelActive for its own "any channel with this peer" case)
// instead of polling GetChannel on a fixed timer (P-1). Unlike
// WaitChannelActive, chansubs here is keyed by the one specific
// channelPoint the caller already knows about (returned by an earlier
// OpenChannel call), not every channel with peerID, so a received signal
// is always about the right channel.
func (c *LndClient) WaitChannelPointActive(peerID []byte, channelPoint wire.OutPoint, deadline time.Time) (*lightning.GetChannelResult, error) {
	point := channelPoint.String()

	if result, err := c.GetChannel(peerID, channelPoint); err == nil {
		return result, nil
	}

	signal := make(chan struct{}, 10)
	defer close(signal)

	c.submtx.Lock()
	subid := c.index
	c.index++
	subs, ok := c.chansubs[point]
	if !ok {
		subs = make(map[uint64]chan struct{})
		c.chansubs[point] = subs
	}
	subs[subid] = signal
	c.submtx.Unlock()

	defer func() {
		c.submtx.Lock()
		subs, ok := c.chansubs[point]
		if ok {
			delete(subs, subid)
			if len(subs) == 0 {
				delete(c.chansubs, point)
			}
		}
		c.submtx.Unlock()
	}()

	// Re-check after subscribing, closing the race window between the
	// first check above and the subscription being registered.
	if result, err := c.GetChannel(peerID, channelPoint); err == nil {
		return result, nil
	}

	for {
		select {
		case <-signal:
			// The subscription is keyed by this exact channelPoint, so
			// the event was about this channel -- but GetChannel could
			// still miss on a genuine eventual-consistency gap between
			// the event firing and ListChannels reflecting it. Keep
			// waiting on a further signal rather than giving up on the
			// first miss.
			if result, err := c.GetChannel(peerID, channelPoint); err == nil {
				return result, nil
			}
		case <-time.After(time.Until(deadline)):
			return nil, fmt.Errorf("deadline exceeded waiting for channel %s to become active", point)
		}
	}
}

func (c *LndClient) GetClosedChannels(nodeID string, channelPoints map[string]uint64) (map[string]uint64, error) {
	r := make(map[string]uint64)
	if len(channelPoints) == 0 {
		return r, nil
	}
	waitingCloseChannels, err := c.getWaitingCloseChannels(nodeID)
	if err != nil {
		return nil, err
	}
	wcc := make(map[string]struct{})
	for _, c := range waitingCloseChannels {
		wcc[c.Channel.ChannelPoint] = struct{}{}
	}
	for c, h := range channelPoints {
		if _, ok := wcc[c]; !ok {
			r[c] = h
		}
	}
	return r, nil
}

func (c *LndClient) getWaitingCloseChannels(nodeID string) ([]*lnrpc.PendingChannelsResponse_WaitingCloseChannel, error) {
	pendingResponse, err := c.client.PendingChannels(context.Background(), &lnrpc.PendingChannelsRequest{})
	if err != nil {
		return nil, err
	}
	var waitingCloseChannels []*lnrpc.PendingChannelsResponse_WaitingCloseChannel
	for _, p := range pendingResponse.WaitingCloseChannels {
		if p.Channel.RemoteNodePub == nodeID {
			waitingCloseChannels = append(waitingCloseChannels, p)
		}
	}
	return waitingCloseChannels, nil
}

// GetPeerId reads from the cache refreshPeerCache maintains (P-2) instead
// of calling ListChannels inline -- this is called on every intercepted
// HTLC.
func (c *LndClient) GetPeerId(scid *lightning.ShortChannelID) ([]byte, error) {
	c.peerCacheMtx.RLock()
	defer c.peerCacheMtx.RUnlock()
	return c.scidToPeer[uint64(*scid)], nil
}

func (c *LndClient) WaitOnline(peerID []byte, deadline time.Time) error {
	pkStr := hex.EncodeToString(peerID)
	signal := make(chan struct{}, 10)
	defer close(signal)

	c.submtx.Lock()
	subid := c.index
	c.index++
	subs, ok := c.peersubs[pkStr]
	if !ok {
		subs = make(map[uint64]chan struct{})
		c.peersubs[pkStr] = subs
	}

	subs[subid] = signal
	c.submtx.Unlock()

	defer func() {
		c.submtx.Lock()
		subs, ok := c.peersubs[pkStr]
		if ok {
			delete(subs, subid)
			if len(subs) == 0 {
				delete(c.peersubs, pkStr)
			}
		}
		c.submtx.Unlock()
	}()

	connected, err := c.IsConnected(peerID)
	if err != nil {
		return err
	}
	if connected {
		return nil
	}

	select {
	case <-signal:
		return nil
	case <-time.After(time.Until(deadline)):
		return fmt.Errorf("deadline exceeded")
	}
}

func (c *LndClient) WaitChannelActive(peerID []byte, deadline time.Time) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Fetch the channels for this peer
	chans, err := c.client.ListChannels(ctx, &lnrpc.ListChannelsRequest{
		Peer: peerID,
	})
	if err != nil {
		return err
	}
	if len(chans.Channels) == 0 {
		return fmt.Errorf("no channels with peer")
	}

	// Exit now if a channel is already active.
	for _, ch := range chans.Channels {
		if ch.Active {
			return nil
		}
	}

	signal := make(chan struct{}, 10)
	defer close(signal)

	// Subscribe to channel active events from this channel
	c.submtx.Lock()
	for _, ch := range chans.Channels {
		chansignal := make(chan struct{}, 10)
		defer close(chansignal)

		// forward signals from all channels to the signal aggregate
		go func(c chan struct{}) {
			for msg := range c {
				signal <- msg
			}
		}(chansignal)

		outpoint := ch.ChannelPoint
		subid := c.index
		c.index++
		subs, ok := c.chansubs[outpoint]
		if !ok {
			subs = make(map[uint64]chan struct{})
			c.chansubs[outpoint] = subs
		}

		subs[subid] = chansignal
		defer func() {
			c.submtx.Lock()
			subs, ok := c.chansubs[outpoint]
			if ok {
				delete(subs, subid)
				if len(subs) == 0 {
					delete(c.chansubs, outpoint)
				}
			}
			c.submtx.Unlock()
		}()
	}
	c.submtx.Unlock()

	// Fetch the channels for this peer again, so there is no gap between the
	// subscription and the call.
	chans, err = c.client.ListChannels(ctx, &lnrpc.ListChannelsRequest{
		Peer: peerID,
	})
	if err != nil {
		return err
	}

	// Exit now if a channel is already active.
	for _, ch := range chans.Channels {
		if ch.Active {
			return nil
		}
	}

	select {
	case <-signal:
		return nil
	case <-time.After(time.Until(deadline)):
		return fmt.Errorf("deadline exceeded")
	}
}

func (c *LndClient) ListChannels() ([]*lightning.Channel, error) {
	channels, err := c.client.ListChannels(
		context.TODO(),
		&lnrpc.ListChannelsRequest{},
	)
	if err != nil {
		return nil, err
	}
	pendingChannels, err := c.client.PendingChannels(
		context.TODO(),
		&lnrpc.PendingChannelsRequest{},
	)
	if err != nil {
		return nil, err
	}

	result := make([]*lightning.Channel, len(channels.Channels))
	for i, c := range channels.Channels {
		peerId, err := hex.DecodeString(c.RemotePubkey)
		if err != nil {
			log.Debug().Err(err).Msg("LND: hex.DecodeString failed in ListChannels")
			continue
		}
		alias, confirmedScid := mapScidsFromChannel(c)
		outpoint, err := lightning.NewOutPointFromString(c.ChannelPoint)
		if err != nil {
			log.Debug().Err(err).Str("point", c.ChannelPoint).Msg("LND: failed to parse outpoint in ListChannels")
		}

		// Map pending HTLCs
		var pendingHtlcs []lightning.HTLC
		for _, h := range c.PendingHtlcs {
			pendingHtlcs = append(pendingHtlcs, lightning.HTLC{
				Incoming:         h.Incoming,
				AmountMloki:      uint64(h.Amount),
				HashLock:         h.HashLock,
				ExpirationHeight: h.ExpirationHeight,
				HtlcIndex:        h.HtlcIndex,
			})
		}

		result[i] = &lightning.Channel{
			AliasScid:     alias,
			ConfirmedScid: confirmedScid,
			ChannelPoint:  outpoint,
			PeerId:        peerId,
			PendingHtlcs:  pendingHtlcs,
			Active:        c.Active,
			Capacity:      c.Capacity,
			LocalBalance:  c.LocalBalance,
			RemoteBalance: c.RemoteBalance,
		}
	}

	for _, c := range pendingChannels.PendingOpenChannels {
		peerId, err := hex.DecodeString(c.Channel.RemoteNodePub)
		if err != nil {
			log.Debug().Err(err).Msg("LND: hex.DecodeString failed in ListChannels (active)")
			continue
		}

		outpoint, err := lightning.NewOutPointFromString(c.Channel.ChannelPoint)
		if err != nil {
			log.Debug().Err(err).Str("point", c.Channel.ChannelPoint).Msg("LND: failed to parse outpoint in ListChannels (active)")
		}
		result = append(result, &lightning.Channel{
			AliasScid:     nil,
			ConfirmedScid: nil,
			ChannelPoint:  outpoint,
			PeerId:        peerId,
			Active:        false, // pending-open: not yet usable, excluded from liquidity aggregation
			Capacity:      c.Channel.Capacity,
			LocalBalance:  c.Channel.LocalBalance,
			RemoteBalance: c.Channel.RemoteBalance,
		})
	}

	return result, nil
}

func (c *LndClient) ListPeers() ([]*lightning.Peer, error) {
	resp, err := c.client.ListPeers(context.Background(), &lnrpc.ListPeersRequest{})
	if err != nil {
		return nil, err
	}

	result := make([]*lightning.Peer, len(resp.Peers))
	for i, p := range resp.Peers {
		alias := ""
		color := ""

		// Attempt to fetch node info for better UI experience (alias & color)
		nodeInfo, err := c.client.GetNodeInfo(context.Background(), &lnrpc.NodeInfoRequest{
			PubKey: p.PubKey,
		})
		if err == nil && nodeInfo.Node != nil {
			alias = nodeInfo.Node.Alias
			color = nodeInfo.Node.Color
		}

		result[i] = &lightning.Peer{
			Pubkey:        p.PubKey,
			Address:       p.Address,
			Inbound:       p.Inbound,
			BytesSent:     p.BytesSent,
			BytesReceived: p.BytesRecv,
			LokiSent:      uint64(p.SatSent),
			LokiReceived:  uint64(p.SatRecv),
			Active:        true, // If they are in ListPeers, they are connected
			Alias:         alias,
			Color:         color,
		}
	}
	return result, nil
}

func (c *LndClient) GetNodeInfo(pubkey string) (*lightning.NodeInfo, error) {
	resp, err := c.client.GetNodeInfo(context.Background(), &lnrpc.NodeInfoRequest{
		PubKey: pubkey,
	})
	if err != nil {
		return nil, err
	}
	if resp.Node == nil {
		return nil, fmt.Errorf("node not found")
	}
	return &lightning.NodeInfo{
		Pubkey: resp.Node.PubKey,
		Alias:  resp.Node.Alias,
		Color:  resp.Node.Color,
	}, nil
}

func (c *LndClient) GetBlockHeight() (uint32, error) {

	info, err := c.client.GetInfo(context.Background(), &lnrpc.GetInfoRequest{})
	if err != nil {
		return 0, err
	}
	return info.BlockHeight, nil
}

func mapScidsFromChannel(c *lnrpc.Channel) (*lightning.ShortChannelID, *lightning.ShortChannelID) {
	var alias *lightning.ShortChannelID
	var confirmedScid *lightning.ShortChannelID
	if c.ZeroConf {
		if c.ZeroConfConfirmedScid != 0 {
			confirmedScid = (*lightning.ShortChannelID)(&c.ZeroConfConfirmedScid)
		}
		alias = (*lightning.ShortChannelID)(&c.ChanId)
	} else {
		confirmedScid = (*lightning.ShortChannelID)(&c.ChanId)
		if len(c.AliasScids) > 0 {
			alias = (*lightning.ShortChannelID)(&c.AliasScids[0])
		}
	}

	return alias, confirmedScid
}

func (c *LndClient) SendCustomMessage(ctx context.Context, msg *lnrpc.SendCustomMessageRequest) (*lnrpc.SendCustomMessageResponse, error) {
	return c.client.SendCustomMessage(ctx, msg)
}

func (c *LndClient) SubscribeCustomMessages(ctx context.Context, req *lnrpc.SubscribeCustomMessagesRequest) (lnrpc.Lightning_SubscribeCustomMessagesClient, error) {
	return c.client.SubscribeCustomMessages(ctx, req)
}

func (c *LndClient) AddInvoice(ctx context.Context, req *lnrpc.Invoice) (*lnrpc.AddInvoiceResponse, error) {
	return c.client.AddInvoice(ctx, req)
}

// AddHoldInvoice creates a hold invoice tied to req.Hash: unlike AddInvoice,
// LND never sees the preimage and cannot settle this invoice on its own --
// the caller must hold onto the preimage and call SettleInvoice or
// CancelInvoice once it knows whether to accept the payment.
func (c *LndClient) AddHoldInvoice(ctx context.Context, req *invoicesrpc.AddHoldInvoiceRequest) (*invoicesrpc.AddHoldInvoiceResp, error) {
	return c.invoicesClient.AddHoldInvoice(ctx, req)
}

func (c *LndClient) SettleInvoice(ctx context.Context, preimage []byte) error {
	_, err := c.invoicesClient.SettleInvoice(ctx, &invoicesrpc.SettleInvoiceMsg{Preimage: preimage})
	return err
}

func (c *LndClient) CancelInvoice(ctx context.Context, paymentHash []byte) error {
	_, err := c.invoicesClient.CancelInvoice(ctx, &invoicesrpc.CancelInvoiceMsg{PaymentHash: paymentHash})
	return err
}

// LookupInvoiceState decodes the payment request to obtain the payment hash,
// then queries LND directly for the invoice's current state.
func (c *LndClient) LookupInvoiceState(ctx context.Context, paymentRequest string) (lightning.InvoiceState, error) {
	decoded, err := c.client.DecodePayReq(ctx, &lnrpc.PayReqString{PayReq: paymentRequest})
	if err != nil {
		return lightning.InvoiceStateOpen, fmt.Errorf("DecodePayReq: %w", err)
	}
	hashBytes, err := hex.DecodeString(decoded.PaymentHash)
	if err != nil {
		return lightning.InvoiceStateOpen, fmt.Errorf("decode payment hash: %w", err)
	}
	inv, err := c.client.LookupInvoice(ctx, &lnrpc.PaymentHash{RHash: hashBytes})
	if err != nil {
		return lightning.InvoiceStateOpen, fmt.Errorf("LookupInvoice: %w", err)
	}
	return convertInvoiceState(inv.State), nil
}

// LookupInvoiceStateByHash is LookupInvoiceState's cheaper sibling (H-11):
// skips the DecodePayReq round-trip when the caller already has the
// payment hash (e.g. derived locally from a hold invoice's stored
// preimage), rather than deriving it from a bolt11 string every time.
func (c *LndClient) LookupInvoiceStateByHash(ctx context.Context, paymentHash []byte) (lightning.InvoiceState, error) {
	inv, err := c.client.LookupInvoice(ctx, &lnrpc.PaymentHash{RHash: paymentHash})
	if err != nil {
		return lightning.InvoiceStateOpen, fmt.Errorf("LookupInvoice: %w", err)
	}
	return convertInvoiceState(inv.State), nil
}

func (c *LndClient) WalletBalance(ctx context.Context) (*lightning.WalletBalanceResult, error) {
	resp, err := c.client.WalletBalance(ctx, &lnrpc.WalletBalanceRequest{})
	if err != nil {
		return nil, err
	}
	return &lightning.WalletBalanceResult{
		TotalBalance:       resp.TotalBalance,
		ConfirmedBalance:   resp.ConfirmedBalance,
		UnconfirmedBalance: resp.UnconfirmedBalance,
	}, nil
}

func (c *LndClient) RegisterChannelAlias(ctx context.Context, baseSCID uint64, phantomSCID uint64) error {
	_, err := c.routerClient.XAddLocalChanAliases(ctx, &routerrpc.AddAliasesRequest{
		AliasMaps: []*lnrpc.AliasMap{
			{BaseScid: baseSCID, Aliases: []uint64{phantomSCID}},
		},
	})
	return err
}

func (c *LndClient) QueryRoutes(destination []byte, amountMloki uint64, outgoingChanId uint64) ([]*lightning.Route, error) {
	pubKey := hex.EncodeToString(destination)
	req := &lnrpc.QueryRoutesRequest{
		PubKey:            pubKey,
		AmtMsat:           int64(amountMloki),
		UseMissionControl: false,
	}
	if outgoingChanId != 0 {
		req.OutgoingChanIds = []uint64{outgoingChanId}
	}

	resp, err := c.client.QueryRoutes(context.Background(), req)
	if err != nil {
		return nil, err
	}

	var routes []*lightning.Route
	for _, r := range resp.Routes {
		var hops []*lightning.Hop
		for _, h := range r.Hops {
			hops = append(hops, &lightning.Hop{
				ChanId:           h.ChanId,
				ChanCapacity:     h.ChanCapacity,
				AmtToForward:     h.AmtToForward,
				Fee:              h.Fee,
				Expiry:           h.Expiry,
				AmtToForwardMsat: h.AmtToForwardMsat,
				FeeMsat:          h.FeeMsat,
				PubKey:           h.PubKey,
				CustomRecords:    h.CustomRecords,
			})
		}

		routes = append(routes, &lightning.Route{
			TotalTimeLock: r.TotalTimeLock,
			TotalFees:     r.TotalFees,
			TotalAmt:      r.TotalAmt,
			Hops:          hops,
		})
	}
	return routes, nil
}

// IsChannelRoutable reports whether scid has both directional policies set
// in the graph, via GetChanInfo -- which (like DescribeGraph, what upstream
// LND's own zero-conf itest polls for this exact check) reads straight from
// the graph DB rather than the router's in-memory pathfinding cache
// QueryRoutes consults. See the lightning.Client interface doc for why this
// is preferred over QueryRoutes for a channel-readiness check.
func (c *LndClient) IsChannelRoutable(scid uint64) (bool, error) {
	edge, err := c.client.GetChanInfo(context.Background(), &lnrpc.ChanInfoRequest{ChanId: scid})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, err
	}
	return edge.Node1Policy != nil && edge.Node2Policy != nil, nil
}

func (c *LndClient) IsChannelPolicyApplied(scid uint64, peerPubkey []byte, timeLockDelta uint32) (bool, error) {
	edge, err := c.client.GetChanInfo(context.Background(), &lnrpc.ChanInfoRequest{ChanId: scid})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, err
	}

	peerHex := hex.EncodeToString(peerPubkey)
	var ourPolicy *lnrpc.RoutingPolicy
	switch peerHex {
	case edge.Node1Pub:
		ourPolicy = edge.Node2Policy
	case edge.Node2Pub:
		ourPolicy = edge.Node1Policy
	default:
		return false, fmt.Errorf("peer %s is neither side of channel %d", peerHex, scid)
	}

	return ourPolicy != nil && ourPolicy.TimeLockDelta == timeLockDelta, nil
}
