package itest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flokiorg/flnd/lnrpc"
	"github.com/flokiorg/flnd/lnrpc/routerrpc"
	"github.com/flokiorg/lokihub/lspd/lightning"
	"github.com/flokiorg/lokihub/lspd/lsps"
	translsps "github.com/flokiorg/lokihub/lspd/transport/lsps"
)

// CustomMessageBus is a minimal LSPS0 (bLIP-50) transport client: it sends
// JSON-RPC requests as custom messages (TLV type translsps.LspsMessageId)
// to a peer and correlates replies by JSON-RPC id, mirroring exactly what
// transport/lsps.Server does on the LSP side (see transport/lsps/server.go's
// subscribeAndListen/handleMessage) but from the client's side of the wire.
type CustomMessageBus struct {
	raw lnrpc.LightningClient

	mu      sync.Mutex
	pending map[string]chan json.RawMessage
}

// randomRequestID generates a request id unique enough to never collide
// with a stale reply left over from a previous test run (see the comment
// in Call).
func randomRequestID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// NewCustomMessageBus subscribes to the peer's custom-message stream in the
// background and starts routing replies to whichever Call is waiting on
// that request id. The subscription lives for the lifetime of ctx.
func NewCustomMessageBus(ctx context.Context, raw lnrpc.LightningClient) (*CustomMessageBus, error) {
	stream, err := raw.SubscribeCustomMessages(ctx, &lnrpc.SubscribeCustomMessagesRequest{})
	if err != nil {
		return nil, fmt.Errorf("subscribing to custom messages: %w", err)
	}

	b := &CustomMessageBus{
		raw:     raw,
		pending: make(map[string]chan json.RawMessage),
	}

	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if msg.Type != translsps.LspsMessageId {
				continue
			}
			b.dispatch(msg.Data)
		}
	}()

	return b, nil
}

func (b *CustomMessageBus) dispatch(data []byte) {
	var resp lsps.JsonRpcResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return
	}
	id, ok := resp.Id.(string)
	if !ok {
		return
	}

	b.mu.Lock()
	ch, found := b.pending[id]
	if found {
		delete(b.pending, id)
	}
	b.mu.Unlock()

	if found {
		ch <- data
	}
}

// Call sends a JSON-RPC request to peer over the custom-message transport
// and blocks until a correlated reply arrives or timeout elapses.
func (b *CustomMessageBus) Call(ctx context.Context, peer []byte, method string, params interface{}, timeout time.Duration) (json.RawMessage, error) {
	// Random rather than a simple counter: a low, restart-from-1 id could
	// coincide with one a leaked flspd process from an earlier test run
	// (see killStrayFlspdProcesses) is also using against the same
	// long-lived devnet peer, matching this call to the wrong reply.
	id, err := randomRequestID()
	if err != nil {
		return nil, fmt.Errorf("generating request id: %w", err)
	}
	b.mu.Lock()
	replyCh := make(chan json.RawMessage, 1)
	b.pending[id] = replyCh
	b.mu.Unlock()

	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshaling params: %w", err)
	}

	req := lsps.JsonRpcRequest{
		JsonRpc: "2.0",
		Method:  method,
		Params:  paramsBytes,
		Id:      id,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	_, err = b.raw.SendCustomMessage(ctx, &lnrpc.SendCustomMessageRequest{
		Peer: peer,
		Type: translsps.LspsMessageId,
		Data: reqBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("sending custom message: %w", err)
	}

	select {
	case data := <-replyCh:
		var resp lsps.JsonRpcResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("unmarshaling reply envelope: %w", err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("LSPS error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		resultBytes, err := json.Marshal(resp.Result)
		if err != nil {
			return nil, fmt.Errorf("re-marshaling result: %w", err)
		}
		return resultBytes, nil
	case <-time.After(timeout):
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, fmt.Errorf("timed out waiting for %s reply from peer", method)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Lsps2Client drives the buyer side of an LSPS2 JIT-channel purchase
// against a single LSP peer, exactly as a wallet client would.
type Lsps2Client struct {
	bus       *CustomMessageBus
	lspPubkey []byte
}

func NewLsps2Client(bus *CustomMessageBus, lspPubkeyHex string) (*Lsps2Client, error) {
	pk, err := hex.DecodeString(lspPubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("decoding LSP pubkey: %w", err)
	}
	return &Lsps2Client{bus: bus, lspPubkey: pk}, nil
}

// GetInfo fetches the LSP's menu of opening_fee_params for lsps2.buy.
func (c *Lsps2Client) GetInfo(ctx context.Context, token string) ([]lsps.Lsps2OpeningFeeParams, error) {
	result, err := c.bus.Call(ctx, c.lspPubkey, "lsps2.get_info", lsps.Lsps2GetInfoLegacyParams{Token: token}, 15*time.Second)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Menu []lsps.Lsps2OpeningFeeParams `json:"opening_fee_params_menu"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshaling get_info result: %w", err)
	}
	return parsed.Menu, nil
}

// Buy purchases a JIT channel quote for paymentSizeMloki using the given
// fee params (normally the first entry from GetInfo), returning the
// phantom SCID and CLTV delta needed to build the routing-hinted invoice.
func (c *Lsps2Client) Buy(ctx context.Context, params lsps.Lsps2OpeningFeeParams, paymentSizeMloki uint64) (*lsps.Lsps2BuyResult, error) {
	buyParams := lsps.Lsps2BuyParams{
		OpeningFeeParams: params,
		PaymentSizeMloki: strconv.FormatUint(paymentSizeMloki, 10),
	}
	result, err := c.bus.Call(ctx, c.lspPubkey, "lsps2.buy", buyParams, 15*time.Second)
	if err != nil {
		return nil, err
	}

	var buyResult lsps.Lsps2BuyResult
	if err := json.Unmarshal(result, &buyResult); err != nil {
		return nil, fmt.Errorf("unmarshaling buy result: %w", err)
	}
	return &buyResult, nil
}

// CreateJitInvoice builds a bolt11 invoice for paymentSizeMloki (the amount
// the payer will actually send) with a single route hint through the LSP at
// the phantom JIT SCID lsps2.buy returned — this is what tells the payer's
// router "route through this node using this (not-yet-real) channel," which
// is what triggers flspd's HTLC interceptor to open the real channel on
// receipt.
//
// This LSP only implements LSPS2's client_trusts_lsp=false mode (see
// handler_lsps2.go's Lsps2BuyResult, which hardcodes ClientTrustsLsp:
// false), meaning it forwards paymentSizeMloki *minus its own opening fee*,
// not the full amount. The invoice's own amount must therefore be that net
// amount, not the gross payment size, or the client's own link rejects the
// forwarded HTLC outright: the final hop's onion payload would declare the
// gross amount while the actual delivered HTLC carries less, which BOLT04
// treats as underpaying (hop.FinalHtlcInvalidAmount) — reproduced live
// 2026-07-16 ("expected <=4980079681 mSAT, got 5000000000 mSAT") and
// initially misread as a payer-side pathfinding/flakiness bug across
// several earlier sessions before being traced here (see BACKLOG.md). The
// route hint's own FeeBaseMsat carries the opening fee so the payer's
// pathfinding adds it back on top of the net amount when computing how
// much to actually send — exactly how a real LSPS2 wallet derives both
// numbers from the quoted opening_fee_params, replicated here to match
// flspd's own split exactly (interceptor/intercept_handler.go's
// computeJitOutgoingAmount).
func CreateJitInvoice(ctx context.Context, clientRaw lnrpc.LightningClient, buyResult *lsps.Lsps2BuyResult, feeParams lsps.Lsps2OpeningFeeParams, paymentSizeMloki uint64, memo string) (string, []byte, error) {
	scid, err := lightning.NewShortChannelIDFromString(buyResult.JITChannelScid)
	if err != nil {
		return "", nil, fmt.Errorf("parsing jit_channel_scid: %w", err)
	}

	minFeeMloki, err := strconv.ParseUint(feeParams.MinFeeMloki, 10, 64)
	if err != nil {
		return "", nil, fmt.Errorf("parsing min_fee_mloki: %w", err)
	}

	// FLSPD-0013: the LSP computes the opening fee on the GROSS payment size
	// via lsps.ComputeOpeningFee (BLIP-52's ((size*proportional)+999999)/1e6,
	// floored at min_fee) and forwards payment_size - opening_fee. Derive the
	// invoice's net amount and the route hint's fee from the exact same
	// function so this wallet simulation matches the LSP's split -- using the
	// old gross-up formula here would make the client expect a slightly larger
	// forwarded amount than the (now spec-correct) LSP actually forwards.
	fee, err := lsps.ComputeOpeningFee(paymentSizeMloki, minFeeMloki, uint64(feeParams.Proportional))
	if err != nil {
		return "", nil, fmt.Errorf("computing opening fee: %w", err)
	}
	netMloki := paymentSizeMloki - fee

	inv := &lnrpc.Invoice{
		Memo:      memo,
		ValueMsat: int64(netMloki),
		RouteHints: []*lnrpc.RouteHint{
			{
				HopHints: []*lnrpc.HopHint{
					{
						NodeId:          buyResult.LspNodeId,
						ChanId:          uint64(*scid),
						FeeBaseMsat:     uint32(fee),
						CltvExpiryDelta: uint32(buyResult.LspCltvExpiryDelta),
					},
				},
			},
		},
	}

	resp, err := clientRaw.AddInvoice(ctx, inv)
	if err != nil {
		return "", nil, fmt.Errorf("creating invoice: %w", err)
	}
	return resp.PaymentRequest, resp.RHash, nil
}

// PayInvoice pays bolt11 via routerrpc.SendPaymentV2 (SendPaymentSync isn't
// implemented on this fork) and blocks until the payment reaches a
// terminal state, returning an error if it fails or times out.
//
// outgoingChanId pins the first hop explicitly rather than leaving it to
// graph-based pathfinding. This used to be load-bearing: with
// option-scid-alias/zero-conf negotiated identically on every devnet role,
// the payer<->lsp channel_update never propagated to either side at all
// (root-caused to flnd's funding manager mishandling the alias-edge
// delete/re-add for non-zero-conf channels that still negotiated the
// feature -- see BACKLOG.md's T4-1 gossip-propagation finding). That's
// fixed at the devnet-config level now (test/devnet/setup.sh strips those
// features from the payer role), so pathfinding can find this hop on its
// own -- pinning it here is now just cheap, harmless defense in depth
// against picking a worse/stale route. Pass 0 to fall back to normal
// pathfinding.
func PayInvoice(ctx context.Context, router routerrpc.RouterClient, bolt11 string, outgoingChanId uint64, timeout time.Duration) (*lnrpc.Payment, error) {
	var outgoingChanIds []uint64
	if outgoingChanId != 0 {
		outgoingChanIds = []uint64{outgoingChanId}
	}
	stream, err := router.SendPaymentV2(ctx, &routerrpc.SendPaymentRequest{
		PaymentRequest:  bolt11,
		TimeoutSeconds:  int32(timeout.Seconds()),
		FeeLimitMsat:    500_000_000, // generous ceiling; this is a 1-2 hop regtest payment
		MaxParts:        1,           // JIT resolution expects the full amount in one HTLC, not split MPP shards
		OutgoingChanIds: outgoingChanIds,
	})
	if err != nil {
		return nil, fmt.Errorf("starting payment: %w", err)
	}

	for {
		payment, err := stream.Recv()
		if err != nil {
			return nil, fmt.Errorf("payment stream: %w", err)
		}
		switch payment.Status {
		case lnrpc.Payment_SUCCEEDED:
			return payment, nil
		case lnrpc.Payment_FAILED:
			return payment, fmt.Errorf("payment failed: %s", payment.FailureReason)
		}
	}
}

// ConnectPeer tells raw to connect to the peer at addr (a container-internal
// host:port on the devnet's docker network, e.g. "flnd-lsp:9735" — not the
// host-published port, since the connection happens from inside that
// role's own container).
func ConnectPeer(ctx context.Context, raw lnrpc.LightningClient, pubkeyHex, addr string) error {
	_, err := raw.ConnectPeer(ctx, &lnrpc.ConnectPeerRequest{
		Addr: &lnrpc.LightningAddress{
			Pubkey: pubkeyHex,
			Host:   addr,
		},
		Perm: true,
	})
	if err != nil && !isAlreadyConnectedError(err) {
		return err
	}
	return nil
}

// AutoAcceptChannels runs a ChannelAcceptor loop against raw for the
// lifetime of ctx, accepting every incoming channel-open request —
// including as zero-conf whenever the initiator asks for it. flnd (like
// upstream LND) rejects ALL incoming channel opens outright if the
// responder hasn't registered a ChannelAcceptor at all, regardless of
// whether zero-conf was requested (see flnd/docs/zero_conf_channels.md),
// so any devnet role that's meant to receive a channel — the LSP's client
// in the JIT flow, most notably — needs this running before the open is
// attempted.
func AutoAcceptChannels(ctx context.Context, raw lnrpc.LightningClient) error {
	stream, err := raw.ChannelAcceptor(ctx)
	if err != nil {
		return fmt.Errorf("opening ChannelAcceptor stream: %w", err)
	}

	go func() {
		for {
			req, err := stream.Recv()
			if err != nil {
				return
			}
			_ = stream.Send(&lnrpc.ChannelAcceptResponse{
				Accept:        true,
				PendingChanId: req.PendingChanId,
				ZeroConf:      req.WantsZeroConf,
			})
		}
	}()

	return nil
}

// OpenNormalChannel opens a regular (non-JIT) channel from raw to peer with
// capacityLoki, confirmed with minConfs on-chain confirmations. This is what
// gives a "payer" role an actual route to the LSP — JIT only covers the
// LSP's own not-yet-real channel to the client on the *last* hop of a
// payment; the payer still needs pre-existing liquidity to route through.
func OpenNormalChannel(ctx context.Context, raw lnrpc.LightningClient, peerPubkeyHex string, capacityLoki int64, minConfs int32) (*lnrpc.ChannelPoint, error) {
	peerPubkey, err := hex.DecodeString(peerPubkeyHex)
	if err != nil {
		return nil, fmt.Errorf("decoding peer pubkey: %w", err)
	}

	cp, err := raw.OpenChannelSync(ctx, &lnrpc.OpenChannelRequest{
		NodePubkey:         peerPubkey,
		LocalFundingAmount: capacityLoki,
		MinConfs:           minConfs,
	})
	if err != nil {
		return nil, fmt.Errorf("opening channel: %w", err)
	}
	return cp, nil
}

func isAlreadyConnectedError(err error) bool {
	// flnd returns a plain gRPC error string here, not a typed sentinel.
	return err != nil && (strings.Contains(err.Error(), "already connected") || strings.Contains(err.Error(), "already have peer"))
}
